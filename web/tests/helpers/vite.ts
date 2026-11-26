import { createServer, type InlineConfig, type Plugin, type ViteDevServer } from 'vite'

// These suites load modules with `ssrLoadModule`, so Vite's browser dependency
// pre-bundling has nothing to do. It is also the one build step that still shells
// out to esbuild, and on locked-down Windows hosts every esbuild output write
// fails with "Access is denied": the optimizer rejects, and its native helper
// keeps the test process alive after the assertions have already failed, so
// `pnpm test` hangs instead of reporting.
//
// `optimizeDeps.noDiscovery` is not enough: @vitejs/plugin-react-swc's `config`
// hook always adds `react/jsx-dev-runtime` to `optimizeDeps.include`, which
// re-enables the optimizer. Clearing the include list after the config is
// resolved leaves the SWC transform exactly as the app uses it and leaves no
// dependency optimizer behind, so esbuild is never started. The guard in
// `createTestServer` turns a regression into a clear failure instead of a hang.
type OptimizeDepsHost = { optimizeDeps?: { include?: string[] } }

const disableDependencyOptimizer: Plugin = {
  name: 'saker-test:no-dependency-optimizer',
  enforce: 'post',
  configResolved(config) {
    const clear = (target: OptimizeDepsHost | undefined) => {
      if (target?.optimizeDeps) target.optimizeDeps.include = []
    }
    clear(config)
    const environments = (config as { environments?: Record<string, OptimizeDepsHost> }).environments ?? {}
    for (const environment of Object.values(environments)) clear(environment)
  },
}

export async function createTestServer(config: InlineConfig = {}): Promise<ViteDevServer> {
  const server = await createServer({
    appType: 'custom',
    ...config,
    // middleware mode keeps the suites off an HTTP port; no HMR and no websocket
    // keep several test files from fighting over Vite's fixed hot port (24678)
    // and leave no socket to keep the event loop busy when a file finishes.
    server: { middlewareMode: true, hmr: false, ws: false, ...config.server },
    plugins: [...(config.plugins ?? []), disableDependencyOptimizer],
    optimizeDeps: { noDiscovery: true, ...config.optimizeDeps },
  })
  if (server.environments.client.depsOptimizer) {
    await server.close()
    throw new Error('web tests must not run the Vite dependency optimizer: esbuild cannot write on this host (see tests/helpers/vite.ts)')
  }
  return server
}
