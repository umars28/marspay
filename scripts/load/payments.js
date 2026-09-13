import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.MARSPAY_BASE_URL || 'http://127.0.0.1:8099';
const USER = __ENV.MARSPAY_USER || 'usr_load';
const MERCHANT = __ENV.MARSPAY_MERCHANT || 'merch_load';
const RUN = __ENV.MARSPAY_RUN_ID || 'run';

export const options = {
  scenarios: {
    ramp: {
      executor: 'ramping-vus',
      startVUs: 5,
      stages: [
        { duration: '10s', target: 25 },
        { duration: '25s', target: 50 },
        { duration: '5s', target: 0 },
      ],
      gracefulRampDown: '5s',
    },
  },
  thresholds: {
    checks: ['rate>0.995'],
    http_req_failed: ['rate<0.005'],
    'http_req_duration{expected_response:true}': ['p(95)<500', 'p(99)<1000'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'],
};

const payload = JSON.stringify({
  merchant_id: MERCHANT,
  method: 'qris',
  amount: 3200000,
  currency: 'IDR',
});

export default function () {
  const res = http.post(`${BASE}/v1/payments`, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${USER}`,
      'Idempotency-Key': `${RUN}-${__VU}-${__ITER}`,
    },
    tags: { name: 'POST /v1/payments' },
  });

  check(res, {
    'status is 201': (r) => r.status === 201,
    'body has a payment id': (r) => r.status === 201 && r.json('id') !== undefined,
  });
}
