// What a download client's list adds up to: the speed it is downloading at and
// how much is still to come, shown beside the number of downloads.
export interface ClientTotals {
  downSpeed: number
  upSpeed: number
  leftBytes: number
}

export function clientTotals(items: { sizeBytes: number; progress: number; downSpeed: number; upSpeed?: number }[]): ClientTotals {
  const sum: ClientTotals = { downSpeed: 0, upSpeed: 0, leftBytes: 0 }
  for (const it of items) {
    sum.downSpeed += Math.max(0, it.downSpeed)
    sum.upSpeed += Math.max(0, it.upSpeed ?? 0)
    const done = Math.min(100, Math.max(0, it.progress)) / 100
    sum.leftBytes += Math.max(0, it.sizeBytes) * (1 - done)
  }
  return sum
}
