import { useLayoutEffect } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { CommandPalette } from "./CommandPalette";
vi.mock("@/components/ui/command", () => ({
  CommandDialog: ({ open, children, onCloseAutoFocus }: { open: boolean; children: React.ReactNode; onCloseAutoFocus: (event: { preventDefault: () => void }) => void }) => {
    useLayoutEffect(() => { if (open) document.querySelector<HTMLInputElement>('input[aria-label="Search navigation"]')?.focus(); }, [open]);
    return open ? <div>{children}<button onClick={() => onCloseAutoFocus({ preventDefault: vi.fn() })}>Close palette fixture</button></div> : null;
  },
  CommandInput: () => <input aria-label="Search navigation" />,
  CommandList: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  CommandEmpty: () => null,
  CommandSeparator: () => <hr />,
  CommandGroup: ({ heading, children }: { heading: string; children: React.ReactNode }) => <section aria-label={heading}>{children}</section>,
  CommandItem: ({ children, onSelect, value, keywords }: { children: React.ReactNode; onSelect: () => void; value: string; keywords?: string[] }) => <button data-value={value} data-keywords={keywords?.join(" ")} onClick={onSelect}>{children}</button>,
}));
afterEach(cleanup);
function setup(roles: string[] = []) {
  const setView = vi.fn(), onOpenChange = vi.fn();
  render(<CommandPalette roles={roles} open onOpenChange={onOpenChange} snapshot={null} view="runners" setView={setView} theme="dark" toggleTheme={vi.fn()} onRefresh={vi.fn()} onSignOut={vi.fn()} />);
  return { setView, onOpenChange };
}
test("palette shares hosting groups and runner search keywords while preserving destination and current marker", () => {
  const { setView, onOpenChange } = setup();
  expect(screen.getByRole("region", { name: "Hosting" })).toBeTruthy(); expect(screen.getByRole("region", { name: "Advanced" })).toBeTruthy();
  const servers = screen.getByRole("button", { name: "Servers and runners Current" }); expect(servers.getAttribute("data-value")).toBe("Servers and runners"); expect(servers.getAttribute("data-keywords")).toContain("runners");
  fireEvent.click(servers); expect(setView).toHaveBeenCalledWith("runners"); expect(onOpenChange).toHaveBeenCalledWith(false);
  expect(screen.queryByRole("button", { name: "Operations" })).toBeNull();
});
test("system administrator still sees Operations", () => { setup(["system_admin"]); expect(screen.getByRole("button", { name: "Operations" })).toBeTruthy(); });
test("keyboard shortcut preserves native palette toggle", () => { const { onOpenChange } = setup(); fireEvent.keyDown(document, { key: "k", ctrlKey: true }); expect(onOpenChange).toHaveBeenCalledWith(false); });


test("palette closing returns focus to the element that opened it", () => {
  const props = { roles: [], onOpenChange: vi.fn(), snapshot: null, view: "runners" as const, setView: vi.fn(), theme: "dark" as const, toggleTheme: vi.fn(), onRefresh: vi.fn(), onSignOut: vi.fn() };
  const view = render(<><button>Open palette fixture</button><CommandPalette {...props} open={false} /></>);
  const trigger = screen.getByRole("button", { name: "Open palette fixture" }); fireEvent.pointerDown(trigger); trigger.focus();
  view.rerender(<><button>Open palette fixture</button><CommandPalette {...props} open /></>);
  const close = screen.getByRole("button", { name: "Close palette fixture" }); close.focus(); fireEvent.click(close); expect(document.activeElement).toBe(trigger);
});
