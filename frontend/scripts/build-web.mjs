import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const frontend = fileURLToPath(new URL('../', import.meta.url));
const vite = fileURLToPath(new URL('../node_modules/vite/bin/vite.js', import.meta.url));
const result = spawnSync(process.execPath, [vite, 'build', '--config', 'vite.renderer.config.ts', '--base', process.env.AO_WEB_BASE || '/apps/orchestrator/', '--outDir', '../deploy/portal-operator/dist', ...process.argv.slice(2)], {
  cwd: frontend,
  env: { ...process.env, VITE_AO_WEB: '1', VITE_NO_ELECTRON: '0' },
  stdio: 'inherit',
});
if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
