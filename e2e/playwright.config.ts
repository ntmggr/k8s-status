import path from 'node:path';
import { defineConfig } from '@playwright/test';

const REPO_ROOT = path.resolve(__dirname, '..');
const BIN_DIR = path.join(__dirname, '.bin');
const APP_PORT = 18080;
const FIXTURE_PORT = 18001;

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  tablet: { width: 1000, height: 900 },
  phone: { width: 390, height: 844 },
};
const COLOR_SCHEMES = ['light', 'dark'] as const;

// The same switches scripts/local-test.sh turns on in fixture mode, so every section
// of the page has data to render.
const APP_ENV = {
  KUBE_API_URL: `http://127.0.0.1:${FIXTURE_PORT}`,
  PORT: String(APP_PORT),
  ENV_NAME: 'local',
  ROOT_APP_NAME: 'root-app',
  NODE_STATS: 'true',
  PENDING_REASONS: 'true',
  UNMANAGED: 'true',
  MESH_MTLS: 'true',
  MESH_NAMESPACE: 'istio-system',
  AZ_SPREAD: 'true',
  JOBS: 'true',
};

const projects = Object.entries(VIEWPORTS).flatMap(([device, viewport]) =>
  COLOR_SCHEMES.map((colorScheme) => ({
    name: `${device}-${colorScheme}`,
    use: { viewport, colorScheme, deviceScaleFactor: 1 },
  })),
);

export default defineConfig({
  testDir: './tests',
  snapshotPathTemplate: '{testDir}/__screenshots__/{projectName}/{arg}{ext}',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  expect: {
    toHaveScreenshot: { animations: 'disabled', caret: 'hide' },
  },
  use: {
    baseURL: `http://127.0.0.1:${APP_PORT}`,
    trace: 'retain-on-failure',
  },
  projects,
  webServer: [
    {
      command: `go build -o "${BIN_DIR}/fixture-server" ./hack/fixture-server && exec "${BIN_DIR}/fixture-server" -addr 127.0.0.1:${FIXTURE_PORT} -dir testdata`,
      cwd: REPO_ROOT,
      port: FIXTURE_PORT,
      reuseExistingServer: !process.env.CI,
      timeout: 180_000,
    },
    {
      command: `go build -o "${BIN_DIR}/k8s-status" ./cmd/k8s-status && exec "${BIN_DIR}/k8s-status"`,
      cwd: REPO_ROOT,
      url: `http://127.0.0.1:${APP_PORT}/k8s-status/healthz`,
      env: APP_ENV,
      reuseExistingServer: !process.env.CI,
      timeout: 180_000,
    },
  ],
});
