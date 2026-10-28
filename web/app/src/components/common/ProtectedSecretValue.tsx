import { useEffect, useEffectEvent, useRef, useState, type ClipboardEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

const maximumBytes = 65536;
const displayed = (value: string) => value.replace(/\r\n?/g, "\n");
// Textarea selections count CRLF as one displayed character. Retain the
// original characters outside the edited range in the submitted value.
function rawOffset(value: string, position: number): number {
  let raw = 0;
  for (let shown = 0; shown < position && raw < value.length; shown++) {
    raw += value[raw] === "\r" && value[raw + 1] === "\n" ? 2 : 1;
  }
  return raw;
}

type SecretValueProps = { id: string; label: string; value: string; onChange: (value: string) => void; disabled?: boolean };

export function ProtectedSecretValue(props: SecretValueProps) {
  // A disabled editor ends its reveal session and invalidates pending file reads
  // before a later enabled editor can receive them.
  return <SecretValueInput key={`${props.id}:${Boolean(props.disabled)}`} {...props} />;
}

function SecretValueInput({ id, label, value, onChange, disabled = false }: SecretValueProps) {
  const [editor, setEditor] = useState<"hidden" | "empty" | "value">("hidden");
  const visible = editor !== "hidden" && !disabled;
  if (editor === "value" && !value) setEditor("hidden");
  const [error, setError] = useState("");
  const [fileRead, setFileRead] = useState<number | null>(null);
  const loading = fileRead !== null;
  const active = useRef(true);
  const revision = useRef(0);
  const picker = useRef<HTMLInputElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const edit = useRef<{ start: number; end: number; kind: string; data: string | null } | undefined>(undefined);
  const caret = useRef<number | undefined>(undefined);
  useEffect(() => { active.current = true; return () => { active.current = false; revision.current++; }; }, []);
  const beforeInput = useEffectEvent((element: HTMLTextAreaElement, event: Event) => {
    const input = event as InputEvent;
    if (!visible || disabled) return;
    // Native undo retains only textarea-normalized text. Refuse it rather
    // than silently restoring a CRLF value with different bytes.
    if (input.inputType.startsWith("history")) { event.preventDefault(); setError("Use Clear value or paste a replacement. Native undo cannot preserve exact line endings."); return; }
    if (input.inputType === "insertLineBreak" || input.inputType === "insertParagraph") {
      event.preventDefault(); edit.current = undefined;
      commit(value.slice(0, rawOffset(value, element.selectionStart)) + "\n" + value.slice(rawOffset(value, element.selectionEnd)), element.selectionStart + 1);
      return;
    }
    edit.current = { start: element.selectionStart, end: element.selectionEnd, kind: input.inputType, data: input.data };
  });
  useEffect(() => {
    const element = textarea.current;
    if (!element) return;
    const listener = (event: Event) => beforeInput(element, event);
    element.addEventListener("beforeinput", listener);
    return () => element.removeEventListener("beforeinput", listener);
  }, []);
  useEffect(() => {
    if (visible && caret.current !== undefined) { textarea.current?.setSelectionRange(caret.current, caret.current); caret.current = undefined; }
  }, [value, visible]);
  function commit(next: string, selection?: number) {
    revision.current++; setFileRead(null); setError("");
    caret.current = selection;
    if (new TextEncoder().encode(next).byteLength > maximumBytes) {
      onChange(""); setEditor("hidden"); setError("Value cleared. Secret values must not exceed 65536 UTF-8 bytes."); return;
    }
    setEditor((current) => current === "hidden" ? current : next ? "value" : "hidden");
    onChange(next);
  }
  function change(next: string, selection: number) {
    const operation = edit.current; edit.current = undefined;
    if (!operation) {
      if (value.includes("\r")) { setError("This edit could not preserve line endings. Clear the value, paste a replacement, or load a UTF-8 file."); return; }
      commit(next, selection); return;
    }
    let { start, end } = operation;
    let inserted = "";
    if (operation.kind.startsWith("delete")) {
      const removed = displayed(value).length - next.length;
      if (start === end) {
        if (operation.kind.endsWith("Backward")) start -= removed;
        else end += removed;
      }
    } else if (operation.kind.startsWith("insert")) {
      const length = next.length - (displayed(value).length - (end - start));
      inserted = operation.data !== null && displayed(operation.data).length === length ? operation.data : next.slice(start, start + length);
    } else { setError("Use paste or a UTF-8 file for this edit to preserve exact line endings."); return; }
    commit(value.slice(0, rawOffset(value, start)) + inserted + value.slice(rawOffset(value, end)), selection);
  }
  function paste(event: ClipboardEvent<HTMLTextAreaElement>) {
    event.preventDefault();
    if (disabled) return;
    const text = event.clipboardData.getData("text/plain");
    if (!visible) { commit(text); return; }
    const element = event.currentTarget;
    commit(value.slice(0, rawOffset(value, element.selectionStart)) + text + value.slice(rawOffset(value, element.selectionEnd)), element.selectionStart + displayed(text).length);
  }
  function copy(event: ClipboardEvent<HTMLTextAreaElement>, cut = false) {
    event.preventDefault();
    if (!visible || disabled) return;
    const element = event.currentTarget;
    const start = rawOffset(value, element.selectionStart);
    const end = rawOffset(value, element.selectionEnd);
    event.clipboardData.setData("text/plain", value.slice(start, end));
    if (cut) commit(value.slice(0, start) + value.slice(end), element.selectionStart);
  }
  async function loadFile(file: File) {
    const current = ++revision.current;
    onChange(""); setEditor("hidden"); setError("");
    try {
      setFileRead(current);
      if (file.size > maximumBytes) { setError("Value cleared. Secret files must not exceed 65536 bytes."); return; }
      // ignoreBOM preserves a leading UTF-8 BOM as part of the secret value.
      const next = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(await file.arrayBuffer());
      if (!active.current || revision.current !== current) return;
      commit(next);
    } catch {
      if (active.current && revision.current === current) setError("Value cleared. The file must contain valid UTF-8 text.");
    } finally {
      setFileRead((pending) => pending === current ? null : pending);
    }
  }
  return <div className="min-w-0 space-y-2" role="group" aria-label={`${label} entry`}>
    <label htmlFor={id}>{label}</label>
    <Textarea ref={textarea} id={id} value={visible ? displayed(value) : value ? "••••••••" : ""} readOnly={!visible} disabled={disabled} aria-required="true" aria-describedby={`${id}-help`} autoComplete="off" spellCheck={false} className="max-h-64 min-h-24 overflow-auto font-mono" placeholder={visible ? "Type or paste a secret value" : "Paste to replace the value, or show to type"} onChange={(event) => { if (visible) change(event.target.value, event.target.selectionStart); }} onPaste={paste} onCopy={(event) => copy(event)} onCut={(event) => copy(event, true)} onDrop={(event) => { event.preventDefault(); setError("Paste or load a UTF-8 file to preserve exact line endings."); }} />
    <p id={`${id}-help`} className="text-xs text-muted-foreground">Hidden paste replaces the whole value. Show to type or edit. Clipboard and file line endings are preserved; typed Enter inserts LF.</p>
    <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={disabled || loading} aria-pressed={visible} onClick={() => setEditor(visible ? "hidden" : value ? "value" : "empty")}>{visible ? "Hide value" : "Show value to edit"}</Button><Button type="button" variant="outline" disabled={disabled || (!value && !loading)} onClick={() => { commit(""); setEditor("hidden"); if (picker.current) picker.current.value = ""; }}>Clear value</Button></div>
    <label className="block text-sm" htmlFor={`${id}-file`}>Load UTF-8 file</label><Input ref={picker} id={`${id}-file`} aria-label={`Load ${label.toLowerCase()} from UTF-8 file`} type="file" disabled={disabled} onChange={(event) => { const file = event.currentTarget.files?.[0]; event.currentTarget.value = ""; if (file) void loadFile(file); }} />
    {loading ? <p role="status">Reading secret file...</p> : null}
    {error ? <p role="alert">{error}</p> : null}
  </div>;
}
