import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

// Derive runtime bindings from deployment config without depending on, or
// modifying, the separately staged production phone assets.
const config = JSON.parse(readFileSync(new URL("./wrangler.jsonc", import.meta.url), "utf8"));

export default defineConfig({
  plugins: [
    cloudflareTest({
      main: resolve(config.main),
      miniflare: {
        compatibilityDate: config.compatibility_date,
        compatibilityFlags: ["nodejs_compat"],
        durableObjects: Object.fromEntries(config.durable_objects.bindings.map(
          (binding: { name: string; class_name: string }) => [
            binding.name,
            {
              className: binding.class_name,
              useSQLite: config.migrations.some(
                (migration: { new_sqlite_classes?: string[] }) =>
                  migration.new_sqlite_classes?.includes(binding.class_name),
              ),
            },
          ],
        )),
        ratelimits: Object.fromEntries(config.ratelimits.map(
          ({ name, ...settings }: { name: string }) => [name, settings],
        )),
        assets: {
          directory: resolve("test", "fixtures", "public"),
          binding: config.assets.binding,
          run_worker_first: config.assets.run_worker_first,
          assetConfig: {
            html_handling: config.assets.html_handling,
            not_found_handling: config.assets.not_found_handling,
          },
        },
      },
    }),
  ],
  test: {
    include: ["test/**/*.test.ts"],
    testTimeout: 15_000,
    hookTimeout: 15_000,
    fileParallelism: false,
  },
});
