import { useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ProtectedSecretValue } from "./ProtectedSecretValue";

afterEach(cleanup);
function setup(initial = "") {
  const changed = vi.fn();
  function Harness({ disabled = false }: { disabled?: boolean }) {
    const [value, setValue] = useState(initial);
    return <ProtectedSecretValue id="secret" label="Secret value" value={value} onChange={(next) => { changed(next); setValue(next); }} disabled={disabled} />;
  }
  const view = render(<Harness />);
  return { changed, Harness, ...view };
}
const textarea = () => screen.getByLabelText<HTMLTextAreaElement>("Secret value");
const paste = (value: string) => fireEvent.paste(textarea(), { clipboardData: { getData: () => value } });
function nativeEdit(start: number, end: number, next: string, inputType: string, data: string | null = null) {
  textarea().setSelectionRange(start, end);
  const event = new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType, data });
  fireEvent(textarea(), event);
  if (!event.defaultPrevented) fireEvent.change(textarea(), { target: { value: next, selectionStart: start } });
}

test.each(["\n", "\r\n", "\r"])("hidden paste preserves %j, spaces, and trailing line breaks without placing plaintext in the DOM", (newline) => {
  const { changed } = setup();
  const value = `-----BEGIN TEST-----${newline}  content  ${newline}-----END TEST-----${newline}${newline}  `;
  paste(value);
  expect(changed).toHaveBeenLastCalledWith(value);
  expect(textarea().value).toBe("••••••••");
  expect(textarea().readOnly).toBe(true);
  expect(document.body.textContent).not.toContain("BEGIN TEST");
  expect(textarea().outerHTML).not.toContain("BEGIN TEST");
});

test.each([
  [2, 2, "A\nB\nC  \n", "deleteContentBackward", null, "A\nB\rC  \r\n"],
  [1, 1, "A\nB\nC  \n", "deleteContentForward", null, "A\nB\rC  \r\n"],
  [1, 3, "AB\nC  \n", "deleteContentBackward", null, "AB\rC  \r\n"],
  [3, 3, "A\n\nXB\nC  \n", "insertText", "X", "A\r\n\nXB\rC  \r\n"],
  [1, 2, "A\n\nB\nC  \n", "insertLineBreak", null, "A\n\nB\rC  \r\n"],
] as const)("native editing keeps exact raw prefix and suffix for %s:%s", (start, end, next, kind, data, expected) => {
  const { changed } = setup("A\r\n\nB\rC  \r\n");
  fireEvent.click(screen.getByRole("button", { name: "Show value to edit" }));
  nativeEdit(start, end, next, kind, data);
  expect(changed).toHaveBeenLastCalledWith(expected);
});

test("visible selection paste inserts raw CRLF and preserves untouched mixed line endings", () => {
  const { changed } = setup("before\r\nreplace\nafter\r\n  ");
  fireEvent.click(screen.getByRole("button", { name: "Show value to edit" }));
  textarea().setSelectionRange(7, 14);
  paste("new\r\nlines\r\n");
  expect(changed).toHaveBeenLastCalledWith("before\r\nnew\r\nlines\r\n\nafter\r\n  ");
  fireEvent.click(screen.getByRole("button", { name: "Hide value" }));
  expect(textarea().value).toBe("••••••••");
});

test("visible keyboard copy and cut preserve selected CRLF while hidden copy never exposes the value", () => {
  const { changed } = setup("before\r\nselected\r\nafter\n");
  const setData = vi.fn();
  fireEvent.copy(textarea(), { clipboardData: { setData } });
  expect(setData).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Show value to edit" }));
  textarea().setSelectionRange(6, 16);
  fireEvent.copy(textarea(), { clipboardData: { setData } });
  expect(setData).toHaveBeenLastCalledWith("text/plain", "\r\nselected\r\n");
  fireEvent.cut(textarea(), { clipboardData: { setData } });
  expect(changed).toHaveBeenLastCalledWith("beforeafter\n");
});

test("typed LF values use multiline entry and explicit clearing hides the editor", () => {
  const { changed } = setup();
  fireEvent.click(screen.getByRole("button", { name: "Show value to edit" }));
  nativeEdit(0, 0, "line1\nline2\n  ", "insertText", "line1\nline2\n  ");
  expect(changed).toHaveBeenLastCalledWith("line1\nline2\n  ");
  fireEvent.click(screen.getByRole("button", { name: "Clear value" }));
  expect(changed).toHaveBeenLastCalledWith("");
  expect(textarea().value).toBe("");
  expect(textarea().readOnly).toBe(true);
});

test("native undo is refused instead of restoring normalized bytes", () => {
  const { changed } = setup("line1\r\nline2");
  fireEvent.click(screen.getByRole("button", { name: "Show value to edit" }));
  const event = new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType: "historyUndo" });
  fireEvent(textarea(), event);
  expect(event.defaultPrevented).toBe(true);
  expect(changed).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toContain("Native undo cannot preserve exact line endings");
});

function file(bytes: Uint8Array, deferred?: Promise<ArrayBuffer>): File {
  const value = new File([bytes as BlobPart], "test-only.pem");
  Object.defineProperty(value, "arrayBuffer", { value: () => deferred ?? Promise.resolve(bytes.buffer) });
  return value;
}
test("UTF-8 file import preserves BOM, mixed line endings, and trailing whitespace; picker and show state reset", async () => {
  const { changed } = setup("old");
  fireEvent.click(screen.getByRole("button", { name: "Show value to edit" }));
  const value = "\ufeffBEGIN\r\nline\nEND\r\n  ";
  const picker = screen.getByLabelText<HTMLInputElement>("Load secret value from UTF-8 file");
  fireEvent.change(picker, { target: { files: [file(new TextEncoder().encode(value))] } });
  await waitFor(() => expect(changed).toHaveBeenLastCalledWith(value));
  expect(picker.value).toBe(""); expect(textarea().readOnly).toBe(true);
  expect(textarea().value).toBe("••••••••");
});
test.each(["invalid", "large"])("invalid or oversized UTF-8 file %s clears rather than normalizes the value", async (kind) => {
  const { changed } = setup("old");
  fireEvent.change(screen.getByLabelText("Load secret value from UTF-8 file"), { target: { files: [file(kind === "invalid" ? new Uint8Array([0xc3, 0x28]) : new Uint8Array(65537))] } });
  await screen.findByRole("alert");
  expect(changed).toHaveBeenLastCalledWith(""); expect(textarea().value).toBe("");
});
test("a large valid value at the UTF-8 byte limit is preserved", () => {
  const { changed } = setup(); const value = "é".repeat(32768);
  paste(value); expect(changed).toHaveBeenLastCalledWith(value);
  paste(value + "x"); expect(changed).toHaveBeenLastCalledWith("");
});
test.each(["unmount", "clear", "replace", "disabled"])("late file content is ignored after %s", async (context) => {
  let resolve!: (value: ArrayBuffer) => void;
  const pending = new Promise<ArrayBuffer>((done) => { resolve = done; });
  const { changed, Harness, unmount, rerender } = setup();
  fireEvent.change(screen.getByLabelText("Load secret value from UTF-8 file"), { target: { files: [file(new Uint8Array([65]), pending)] } });
  if (context === "unmount") unmount();
  if (context === "clear") fireEvent.click(screen.getByRole("button", { name: "Clear value" }));
  if (context === "replace") paste("new");
  if (context === "disabled") rerender(<Harness disabled />);
  await act(async () => resolve(new TextEncoder().encode("late-test-value").buffer));
  expect(changed.mock.calls.some(([value]) => value === "late-test-value")).toBe(false);
});
