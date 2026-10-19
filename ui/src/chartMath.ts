// Round a step up to 1, 2 or 5 times a power of ten so gridlines land on readable numbers.
export function niceStep(raw: number) {
  if (!(raw > 1)) return 1
  const power = 10 ** Math.floor(Math.log10(raw))
  const unit = raw / power
  return (unit <= 1 ? 1 : unit <= 2 ? 2 : unit <= 5 ? 5 : 10) * power
}
