import type { ReactNode } from "react";

export function HelpDetails({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <details className="min-w-0 text-sm text-muted-foreground">
      <summary className="w-fit cursor-pointer rounded font-medium text-foreground">
        {title}
      </summary>
      <div className="mt-2 min-w-0 space-y-2 text-xs">{children}</div>
    </details>
  );
}
