package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

type controlMessage struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Message string `json:"message,omitempty"`
}

// Mutable peer fields are protected by engine.mu; channel operations never are.
type remotePeer struct {
	engine      *Engine
	sid, kid    string
	name        string
	inviteGen   uint64
	deviceID    string
	request     *PairRequest
	ready       bool
	closed      bool
	awaitingAck bool
	pc          *webrtc.PeerConnection
	control     channelWire
	controlSend sync.Mutex
	streams     map[string]*stream
	channelRate rateLimit
	remote      net.TCPAddr
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	opened      chan struct{}
	approved    chan struct{}
	authorized  chan struct{}
}

func (p *remotePeer) negotiate(sdp string, key []byte, reply func(any) error) error {
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	defer cancel()
	filtered, err := p.engine.filterOffer(ctx, sdp)
	if err != nil {
		return err
	}
	pc, err := p.engine.newPeerConnection()
	if err != nil {
		return err
	}
	p.engine.mu.Lock()
	if p.closed {
		p.engine.mu.Unlock()
		_ = pc.Close()
		return net.ErrClosed
	}
	p.pc = pc
	p.engine.mu.Unlock()
	pc.OnDataChannel(p.onChannel)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			p.shutdown()
		}
	})
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: filtered}); err != nil {
		return fmt.Errorf("invalid remote SDP: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(answer); err != nil {
		return err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return ctx.Err()
	}
	local := pc.LocalDescription()
	if local == nil {
		return errors.New("LAN answer unavailable")
	}
	plain, err := json.Marshal(struct {
		SDP string `json:"sdp"`
	}{local.SDP})
	if err != nil {
		return err
	}
	env, err := sealSignal(key, p.engine.room, envelope{
		Type: "signal", SID: p.sid, KID: p.kid, Kind: "answer",
	}, plain)
	if err != nil {
		return err
	}
	if err = reply(env); err != nil {
		return fmt.Errorf("send encrypted answer: %w", err)
	}
	return nil
}

func (p *remotePeer) lifecycle() {
	defer p.shutdown()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-p.opened:
	case <-p.done:
		return
	case <-timer.C:
		p.engine.report(errors.New("LAN connection/control channel timed out"))
		return
	}
	if p.kid == "pair" {
		select {
		case <-p.approved:
		case <-p.done:
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(30 * time.Second)
		select {
		case <-p.authorized:
		case <-p.done:
			return
		case <-timer.C:
			p.engine.report(errors.New("paired credential acknowledgement timed out"))
			return
		}
	}
	<-p.done
}

func (p *remotePeer) shutdown() {
	e := p.engine
	e.mu.Lock()
	if p.closed {
		e.mu.Unlock()
		return
	}
	p.closed, p.ready, p.request = true, false, nil
	delete(e.peers, p.sid)
	pc := p.pc
	streams := make([]*stream, 0, len(p.streams))
	for _, s := range p.streams {
		streams = append(streams, s)
	}
	close(p.done)
	if p.cancel != nil {
		p.cancel()
	}
	e.mu.Unlock()
	for _, s := range streams {
		s.abort(net.ErrClosed)
	}
	if pc != nil {
		_ = pc.Close()
	}
	e.changed()
}

func (p *remotePeer) sendControl(message controlMessage) error {
	p.controlSend.Lock()
	defer p.controlSend.Unlock()
	return p.sendControlLocked(message)
}

func (p *remotePeer) sendControlLocked(message controlMessage) error {
	p.engine.mu.Lock()
	dc, closed := p.control, p.closed
	p.engine.mu.Unlock()
	if closed || dc == nil {
		return net.ErrClosed
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return dc.SendText(string(data))
}

func reliable(dc *webrtc.DataChannel) bool {
	return dc.Ordered() && dc.MaxRetransmits() == nil && dc.MaxPacketLifeTime() == nil &&
		!dc.Negotiated() && dc.Protocol() == ""
}

func (p *remotePeer) onChannel(dc *webrtc.DataChannel) {
	e := p.engine
	label := dc.Label()
	if len(label) > 64 || !reliable(dc) {
		_ = dc.Close()
		p.shutdown()
		return
	}
	e.mu.Lock()
	if p.closed || !p.channelRate.allow(time.Now(), 16, 32) {
		e.mu.Unlock()
		_ = dc.Close()
		p.shutdown()
		return
	}
	if label == "shareme.control" {
		if p.control != nil {
			e.mu.Unlock()
			_ = dc.Close()
			p.shutdown()
			return
		}
		p.control = dc
		e.mu.Unlock()
		dc.OnOpen(p.controlOpened)
		dc.OnMessage(p.controlReceived)
		dc.OnClose(p.shutdown)
		dc.OnError(func(error) { p.shutdown() })
		return
	}
	e.mu.Unlock()
	remote, err := p.selectedAddress()
	if err != nil {
		_ = dc.Close()
		p.shutdown()
		return
	}
	e.mu.Lock()
	_, duplicate := p.streams[label]
	_, exists := e.state.Devices[p.deviceID]
	if p.closed || !validHTTPLabel(label) || !p.ready || !exists || len(p.streams) >= 4 || duplicate {
		e.mu.Unlock()
		_ = dc.Close()
		return
	}
	s := newStream(dc, p.deviceID, remote, func() {
		e.mu.Lock()
		delete(p.streams, label)
		e.mu.Unlock()
	})
	p.streams[label] = s
	e.mu.Unlock()
	dc.OnMessage(func(message webrtc.DataChannelMessage) { s.receive(message.Data, message.IsString) })
	dc.OnClose(s.transportClosed)
	dc.OnError(s.abort)
	dc.OnOpen(func() {
		e.mu.Lock()
		_, exists := e.state.Devices[p.deviceID]
		valid := !p.closed && p.ready && exists
		e.mu.Unlock()
		if !valid {
			s.abort(net.ErrClosed)
			return
		}
		e.listener.offer(s)
	})
}

func (p *remotePeer) selectedAddress() (net.TCPAddr, error) {
	p.engine.mu.Lock()
	pc := p.pc
	p.engine.mu.Unlock()
	if pc == nil || pc.SCTP() == nil || pc.SCTP().Transport() == nil {
		return net.TCPAddr{}, errors.New("selected ICE transport unavailable")
	}
	pair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil ||
		!p.engine.ip.Equal(net.ParseIP(pair.Local.Address)) ||
		!p.engine.permittedRemote(net.ParseIP(pair.Remote.Address)) ||
		pair.Local.Protocol != webrtc.ICEProtocolUDP || pair.Remote.Protocol != webrtc.ICEProtocolUDP ||
		pair.Local.Typ != webrtc.ICECandidateTypeHost || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		return net.TCPAddr{}, errors.New("selected ICE pair is outside the selected private LAN")
	}
	return net.TCPAddr{IP: net.ParseIP(pair.Remote.Address).To4(), Port: int(pair.Remote.Port)}, nil
}

func (p *remotePeer) controlOpened() {
	remote, err := p.selectedAddress()
	if err != nil {
		p.engine.report(err)
		p.shutdown()
		return
	}
	e := p.engine
	e.mu.Lock()
	if p.closed {
		e.mu.Unlock()
		return
	}
	p.remote = remote
	close(p.opened)
	if p.kid == "pair" {
		if p.inviteGen != e.inviteGen || len(e.invite) != 32 {
			e.mu.Unlock()
			p.shutdown()
			return
		}
		p.request = &PairRequest{ID: p.sid, Name: p.name, Source: remote.IP.String(), CreatedAt: time.Now().UTC()}
		e.mu.Unlock()
		e.changed()
		return
	}
	e.mu.Unlock()
	p.authorize()
}

func (p *remotePeer) controlReceived(message webrtc.DataChannelMessage) {
	var ack struct {
		Type string `json:"type"`
	}
	if !message.IsString || strictJSON(message.Data, &ack, 256) != nil || ack.Type != "paired-ack" {
		p.shutdown()
		return
	}
	e := p.engine
	e.mu.Lock()
	valid := p.awaitingAck && !p.closed && !p.ready
	if valid {
		p.awaitingAck = false
	}
	e.mu.Unlock()
	if !valid {
		p.shutdown()
		return
	}
	p.authorize()
}

func (p *remotePeer) authorize() {
	e := p.engine
	e.mu.Lock()
	_, exists := e.state.Devices[p.deviceID]
	if p.closed || !exists || p.ready {
		e.mu.Unlock()
		p.shutdown()
		return
	}
	p.ready = true
	close(p.authorized)
	e.mu.Unlock()
	if err := p.sendControl(controlMessage{Type: "ready"}); err != nil {
		p.shutdown()
		return
	}
	e.changed()
}
