import { apiGet, apiSend } from './client'

export interface Waiver {
  member_number: number
  year: number
  month: number
  reason: string
  created_at: string
}

export function fetchWaivers(): Promise<Waiver[]> {
  return apiGet<Waiver[]>('/api/payments/waivers')
}

export function createWaiver(memberNumber: number, year: number, month: number, reason: string): Promise<Waiver> {
  return apiSend<Waiver>('POST', `/api/payments/${memberNumber}/waive`, { year, month, reason })
}

export function deleteWaiver(memberNumber: number, year: number, month: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/payments/${memberNumber}/waive/${year}/${month}`)
}
