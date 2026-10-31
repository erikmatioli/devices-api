// k6 smoke load test: a short mixed workload with latency and error
// thresholds. Run it with `make load` against a running stack.
import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const headers = {
  Authorization: `Bearer ${__ENV.TOKEN}`,
  'Content-Type': 'application/json',
};

export const options = {
  scenarios: {
    mixed: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 20),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{op:create}': ['p(95)<150'],
    'http_req_duration{op:get}': ['p(95)<50'],
    'http_req_duration{op:list}': ['p(95)<100'],
    'http_req_duration{op:patch}': ['p(95)<150'],
  },
};

const brands = ['Google', 'Apple', 'Samsung', 'Nokia'];

// Idempotency keys must be unique per request across runs: reusing one with
// a different body is rejected with 422, by design.
const runId = Date.now().toString(36);

export default function () {
  const brand = brands[Math.floor(Math.random() * brands.length)];

  const created = http.post(`${BASE_URL}/v1/devices`,
    JSON.stringify({ name: `load-${__VU}-${__ITER}`, brand }),
    { headers: { ...headers, 'Idempotency-Key': `k6-${runId}-${__VU}-${__ITER}` }, tags: { op: 'create' } });
  check(created, { 'create is 201': (r) => r.status === 201 });
  if (created.status !== 201) {
    return;
  }
  const id = created.json('id');

  check(http.get(`${BASE_URL}/v1/devices/${id}`, { headers, tags: { op: 'get' } }),
    { 'get is 200': (r) => r.status === 200 });

  check(http.get(`${BASE_URL}/v1/devices?brand=${brand}&limit=20`, { headers, tags: { op: 'list' } }),
    { 'list is 200': (r) => r.status === 200 });

  check(http.patch(`${BASE_URL}/v1/devices/${id}`, JSON.stringify({ state: 'in-use' }),
    { headers, tags: { op: 'patch' } }),
    { 'patch is 200': (r) => r.status === 200 });
}
