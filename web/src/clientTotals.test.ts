import assert from 'node:assert/strict'
import { test } from 'node:test'
import { clientTotals } from './clientTotals.ts'

test('a client list adds up speed and what is left', () => {
  const t = clientTotals([
    { sizeBytes: 1000, progress: 50, downSpeed: 100, upSpeed: 5 },
    { sizeBytes: 2000, progress: 0, downSpeed: 300 },
    { sizeBytes: 500, progress: 100, downSpeed: 0, upSpeed: 20 },
  ])
  assert.deepEqual(t, { downSpeed: 400, upSpeed: 25, leftBytes: 2500 })
})

test('an empty list and odd numbers add up to zero, never below', () => {
  assert.deepEqual(clientTotals([]), { downSpeed: 0, upSpeed: 0, leftBytes: 0 })
  assert.deepEqual(clientTotals([{ sizeBytes: 100, progress: 150, downSpeed: -5 }]), { downSpeed: 0, upSpeed: 0, leftBytes: 0 })
})
