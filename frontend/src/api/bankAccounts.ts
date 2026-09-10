import { apiGet, apiSend } from './client'

export interface BankAccount {
  id: number
  fio_account_id: string
  iban: string | null
  currency: string
  display_name: string
  created_at: string
  has_token: boolean
  is_active: boolean
}

export interface CreateBankAccountPayload {
  fio_account_id: string
  display_name: string
  fio_token: string
  iban?: string
  currency?: string
}

// fio_token omitted (or undefined) leaves the existing token untouched —
// only send it on a deliberate token rotation.
export interface UpdateBankAccountPayload {
  display_name: string
  fio_token?: string
}

export function fetchBankAccounts(): Promise<BankAccount[]> {
  return apiGet<BankAccount[]>('/api/account')
}

export function createBankAccount(payload: CreateBankAccountPayload): Promise<BankAccount> {
  return apiSend<BankAccount>('POST', '/api/account', payload)
}

export function updateBankAccount(id: number, payload: UpdateBankAccountPayload): Promise<BankAccount> {
  return apiSend<BankAccount>('PATCH', `/api/account/${id}`, payload)
}

export function deleteBankAccount(id: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/account/${id}`)
}

export interface SyncResult {
  bank_account_id: number
  transactions_fetched: number
  transactions_inserted: number
}

// Manual "sync now" trigger, on top of the daily scheduled sync. Refused by
// the backend (409) when DISABLE_FIO_SYNC is set.
export function triggerFioSync(id: number): Promise<SyncResult> {
  return apiSend<SyncResult>('POST', `/api/account/${id}/sync`)
}
