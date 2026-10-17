import { defineConfig, devices } from '@playwright/test';
import path from 'node:path';

const root = path.resolve('e2e/.tmp');
export const MEDIA_ROOT = path.join(root, 'media');
const DATA_DIR = path.join(root, 'data');

export default defineConfig({
  testDir: 'e2e',
  timeout: 60_000,
  workers: 1, // tests share one server and media root
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18097',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'mobile', use: { ...devices['Pixel 7'] }, grepInvert: /@desktop/ },
    { name: 'desktop', use: { ...devices['Desktop Chrome'] }, grep: /@desktop/ },
  ],
  webServer: [
    {
      command: 'node e2e/fake-tmdb.mjs',
      url: 'http://127.0.0.1:18080/health',
      reuseExistingServer: false,
    },
    {
      command: `rm -rf ${root} && mkdir -p ${MEDIA_ROOT} ${DATA_DIR} && go build -o e2e/.tmp/media-ingest ./cmd/media-ingest && e2e/.tmp/media-ingest`,
      url: 'http://127.0.0.1:18097/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
      env: {
        LISTEN_ADDR: '127.0.0.1:18097',
        MEDIA_ROOT,
        DATA_DIR,
        AUTH_MODE: 'none',
        AUTH_NONE_CONFIRM: 'true',
        COOKIE_SECURE: 'false',
        TMDB_API_KEY: 'fake-api-key-0123456789',
        TMDB_BASE_URL: 'http://127.0.0.1:18080/3',
        TMDB_IMAGE_BASE_URL: 'http://127.0.0.1:18080/img',
        JELLYFIN_URL: 'http://127.0.0.1:18080/jellyfin',
        JELLYFIN_API_KEY: 'jf-test-key',
        JELLYFIN_PATH_PREFIX: '/jf/media',
        LOG_LEVEL: 'warn',
      },
    },
  ],
});
