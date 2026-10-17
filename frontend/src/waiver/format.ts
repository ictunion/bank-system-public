const MONTH_NAMES = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
] as const

export function monthLabel(year: number, month: number): string {
  return `${MONTH_NAMES[month - 1] ?? month} ${year}`
}

export function formatMemberNumber(memberNumber: number): string {
  return String(memberNumber).padStart(8, '0')
}

export function shortMonthLabel(year: number, month: number): string {
  return `${String(month).padStart(2, '0')}/${year}`
}
