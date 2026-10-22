import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { GitApplicationFields, emptyGitDraft, type GitApplicationDraft } from "./GitApplicationFields";
afterEach(cleanup);
function Editor() { const [value, setValue] = useState<GitApplicationDraft>(emptyGitDraft()); return <><GitApplicationFields value={value} onChange={setValue} disabled={false} /><output>{JSON.stringify(value)}</output></>; }
test("public is explicit by default and private access survives all builder switches", () => { render(<Editor />); expect(screen.getByLabelText<HTMLSelectElement>("Git source access").value).toBe("public"); fireEvent.change(screen.getByLabelText("Git source access"), { target: { value: "https_basic" } }); for (const strategy of ["dockerfile", "railpack", "static"]) { fireEvent.change(screen.getByLabelText("Build strategy"), { target: { value: strategy } }); expect(screen.getByLabelText<HTMLSelectElement>("Git source access").value).toBe("https_basic"); expect(screen.getByRole("status").textContent).toContain(`"credential_kind":"https_basic"`); } expect(screen.queryByLabelText(/password|secret value/i)).toBeNull(); });
