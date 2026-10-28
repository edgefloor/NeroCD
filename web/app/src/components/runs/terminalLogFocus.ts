let pendingOpener: HTMLElement | null = null;

export function rememberTerminalLogOpener(opener: HTMLElement): void {
  pendingOpener = opener;
}

export function takeTerminalLogOpener(): HTMLElement | null {
  const opener = pendingOpener;
  pendingOpener = null;
  return opener;
}
