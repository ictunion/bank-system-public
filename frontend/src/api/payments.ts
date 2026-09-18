import { apiGet, apiSend } from './client'
import type { MonthRef } from './transactions'

export interface Waiver {
  member_number: number
  year: number
  month: number
  reason: string
  created_at: string
}

// Always this shape, whether the request was a single month or a range — a
// single-month call just comes back with one Waived entry and an empty
// Skipped array.
export interface WaiveResult {
  member_number: number
  waived: Waiver[]
  skipped: MonthRef[]
}

export function fetchWaivers(): Promise<Waiver[]> {
  return apiGet<Waiver[]>('/api/payments/waivers')
}

/**
 * end optional — provide it to waive every month from {year, month} through
 * end inclusive. A month in that range already covered by a real payment is
 * skipped (see result.skipped), not an error; without end, that same
 * situation on the single requested month is a 400 instead.
 */
export function createWaiver(
  memberNumber: number,
  year: number,
  month: number,
  reason: string,
  end?: MonthRef,
): Promise<WaiveResult> {
  return apiSend<WaiveResult>('POST', `/api/payments/${memberNumber}/waive`, {
    year,
    month,
    reason,
    end_year: end?.year,
    end_month: end?.month,
  })
}

export function deleteWaiver(memberNumber: number, year: number, month: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/payments/${memberNumber}/waive/${year}/${month}`)
}
