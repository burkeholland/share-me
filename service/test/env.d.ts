import type { Env as ServiceEnv } from "../src";

declare global {
  namespace Cloudflare {
    interface Env extends ServiceEnv {}
    interface GlobalProps {
      mainModule: typeof import("../src");
    }
  }
}
