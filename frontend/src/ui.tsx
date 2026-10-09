import { useEffect, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Icon } from "./icons";
import { dismissToast, useStore } from "./store";
import type { Member, Role } from "./types";
import { cx, hue, initials, ROLES } from "./util";

type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger" | "approve";
  size?: "sm" | "md";
  icon?: string;
  loading?: boolean;
};

export function Button({ variant = "secondary", size = "md", icon, loading, className, children, disabled, ...rest }: BtnProps) {
  return (
    <button className={cx("btn", `btn-${variant}`, `btn-${size}`, className)} disabled={disabled || loading} {...rest}>
      {loading ? <Spinner /> : icon ? <Icon name={icon} size={size === "sm" ? 14 : 16} /> : null}
      {children && <span>{children}</span>}
    </button>
  );
}

export function Spinner({ size = 14 }: { size?: number }) {
  return <span className="spinner" style={{ width: size, height: size }} />;
}

export function Avatar({ member, size = 24, title }: { member: Member | null | undefined; size?: number; title?: string }) {
  const name = member?.display_name || "?";
  const h = hue(member?.id || name);
  return (
    <span
      className="avatar"
      title={title ?? name}
      style={{ width: size, height: size, fontSize: size * 0.4, background: `hsl(${h} 55% 52%)` }}
    >
      {initials(name)}
    </span>
  );
}

export function RoleChip({ role, small }: { role: Role; small?: boolean }) {
  return <span className={cx("role-chip", `role-${role}`, small && "small")}>{ROLES[role].label}</span>;
}

export function Badge({ children, tone = "neutral", icon }: { children: ReactNode; tone?: string; icon?: string }) {
  return (
    <span className={cx("badge", `badge-${tone}`)}>
      {icon && <Icon name={icon} size={12} />}
      {children}
    </span>
  );
}

export function Modal({ title, onClose, children, width = 520, footer }: { title: ReactNode; onClose: () => void; children: ReactNode; width?: number; footer?: ReactNode }) {
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" style={{ width }} role="dialog" aria-modal="true">
        <div className="modal-head">
          <h2>{title}</h2>
          <button className="icon-btn" onClick={onClose} aria-label="Close">
            <Icon name="x" />
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}

export function Field({ label, hint, children, error }: { label: ReactNode; hint?: ReactNode; children: ReactNode; error?: string }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  );
}

export function RolePicker({ value, onChange }: { value: Role[]; onChange: (r: Role[]) => void }) {
  return (
    <div className="role-picker">
      {(Object.keys(ROLES) as Role[]).map((r) => {
        const on = value.includes(r);
        return (
          <button
            type="button"
            key={r}
            className={cx("role-toggle", `role-${r}`, on && "on")}
            onClick={() => onChange(on ? value.filter((x) => x !== r) : [...value, r])}
          >
            {on && <Icon name="check" size={13} />}
            {ROLES[r].label}
          </button>
        );
      })}
    </div>
  );
}

export function Segmented<T extends string>({ value, options, onChange }: { value: T; options: { value: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div className="segmented">
      {options.map((o) => (
        <button type="button" key={o.value} className={cx(o.value === value && "on")} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return (
    <div className="toasts">
      {toasts.map((t) => (
        <div key={t.id} className={cx("toast", `toast-${t.kind}`)} onClick={() => dismissToast(t.id)}>
          <Icon name={t.kind === "error" ? "alert" : t.kind === "success" ? "check" : "sparkle"} />
          <span>{t.text}</span>
        </div>
      ))}
    </div>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  return (
    <Button
      size="sm"
      icon="copy"
      onClick={() => {
        navigator.clipboard?.writeText(text).catch(() => {});
      }}
    >
      {label}
    </Button>
  );
}

export function Empty({ icon, title, children }: { icon: string; title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <div className="empty-icon">
        <Icon name={icon} size={22} />
      </div>
      <h3>{title}</h3>
      {children}
    </div>
  );
}
