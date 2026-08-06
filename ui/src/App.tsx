import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { ApiError, SESSION_EXPIRED_EVENT, type Preferences, type Profile, type PublicConfig, type Session, api } from "./api";
import { Summary } from "./Summary";
import { applyTheme } from "./theme";

const SAFE_ERRORS = {
  login: "We couldn't sign you in. Check your details and try again.",
  invitation: "This invitation is unavailable or has expired.",
  registration: "We couldn't create your account. Review the fields and try again.",
  recovery: "We couldn't complete that request. Please try again later.",
  preferences: "Your changes weren't saved. Review the fields and try again.",
};
const LOADING_CONFIG: PublicConfig = {
  productName: "Workouts Explorer",
  pollingIntervalSeconds: 30,
  mapFitPaddingPixels: 48,
  passwordMinimumLength: 12,
  pageSizeMaximum: 100,
};
const PAGE_SIZE_CHOICES = [25, 50, 75, 100];
const WORKOUT_COLUMN_CHOICES = [
  ["date", "Date"],
  ["type", "Type"],
  ["duration", "Duration"],
  ["distance", "Distance"],
  ["pace", "Pace"],
  ["calories", "Calories"],
  ["heartRate", "Heart rate"],
  ["elevation", "Elevation"],
] as const;
const FALLBACK_TIME_ZONES = [
  "Pacific/Honolulu", "America/Anchorage", "America/Los_Angeles", "America/Denver", "America/Chicago",
  "America/New_York", "America/Halifax", "America/Sao_Paulo", "Atlantic/Azores", "Europe/London",
  "Europe/Paris", "Europe/Athens", "Africa/Nairobi", "Asia/Dubai", "Asia/Karachi", "Asia/Kolkata",
  "Asia/Dhaka", "Asia/Bangkok", "Asia/Shanghai", "Asia/Tokyo", "Australia/Sydney", "Pacific/Auckland",
];

interface TimeZoneOption { value: string; label: string; offsetMinutes: number }

function createTimeZoneOption(timeZone: string): TimeZoneOption {
  let offset = "GMT";
  try {
    offset = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "longOffset" })
      .formatToParts(new Date()).find((part) => part.type === "timeZoneName")?.value ?? "GMT";
  } catch { /* The form validator reports unsupported stored zones. */ }
  const match = /^GMT([+-])(\d{1,2})(?::?(\d{2}))?$/.exec(offset);
  const direction = match?.[1] === "-" ? -1 : 1;
  const hours = Number(match?.[2] ?? 0);
  const minutes = Number(match?.[3] ?? 0);
  const offsetMinutes = direction * (hours * 60 + minutes);
  const sign = offsetMinutes < 0 ? "-" : "+";
  const absolute = Math.abs(offsetMinutes);
  return { value: timeZone, label: `${timeZone} (UTC ${sign}${Math.floor(absolute / 60)}:${String(absolute % 60).padStart(2, "0")})`, offsetMinutes };
}

function sortedTimeZoneOptions(current: string) {
  const supportedValuesOf = (Intl as typeof Intl & { supportedValuesOf?: (key: "timeZone") => string[] }).supportedValuesOf;
  const zones = supportedValuesOf ? supportedValuesOf("timeZone") : FALLBACK_TIME_ZONES;
  const unique = new Set(zones);
  unique.add(current);
  return [...unique].map(createTimeZoneOption).sort((left, right) => left.offsetMinutes - right.offsetMinutes || left.value.localeCompare(right.value));
}

async function loadPublicConfig() {
  const config = await api<PublicConfig>("/api/config");
  if (!Number.isInteger(config.passwordMinimumLength) || config.passwordMinimumLength < 12 || config.passwordMinimumLength > 64 ||
      !Number.isInteger(config.pageSizeMaximum) || config.pageSizeMaximum < 25 || config.pageSizeMaximum > 1000) {
    throw new Error("Invalid public configuration");
  }
  return config;
}

function usePathname() {
  const [pathname, setPathname] = useState(window.location.pathname + window.location.search);
  useEffect(() => {
    const update = () => setPathname(window.location.pathname + window.location.search);
    window.addEventListener("popstate", update);
    return () => window.removeEventListener("popstate", update);
  }, []);
  return pathname;
}

function navigate(to: string, replace = false) {
  history[replace ? "replaceState" : "pushState"]({}, "", to);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

function AppLink({ to, children, className }: { to: string; children: ReactNode; className?: string }) {
  return <a className={className} href={to} onClick={(event) => { event.preventDefault(); navigate(to); }}>{children}</a>;
}

function Mark({ compact = false }: { compact?: boolean }) {
  return (
    <div className={compact ? "mark mark--compact" : "mark"} aria-hidden="true">
      <span /><span /><span />
    </div>
  );
}

function PublicFrame({ eyebrow, title, intro, children }: { eyebrow: string; title: string; intro: string; children: ReactNode }) {
  return (
    <main className="public-layout">
      <section className="public-story" aria-label="Workouts Explorer">
        <div className="contours" aria-hidden="true" />
        <div className="public-brand"><Mark /> <span>Workouts<br />Explorer</span></div>
        <div className="story-copy">
          <p className="kicker">A lifetime, in motion</p>
          <p>Trace every familiar road, hard-earned summit, and route still waiting beyond the next turn.</p>
        </div>
      </section>
      <section className="auth-panel">
        <div className="mobile-brand"><Mark compact /><strong>Workouts Explorer</strong></div>
        <div className="auth-card">
          <p className="eyebrow">{eyebrow}</p>
          <h1>{title}</h1>
          <p className="lede">{intro}</p>
          {children}
        </div>
        <p className="auth-foot">Private by design. Your routes remain yours.</p>
      </section>
    </main>
  );
}

function ErrorSummary({ message, summaryRef }: { message?: string; summaryRef: React.RefObject<HTMLDivElement | null> }) {
  if (!message) return null;
  return <div className="error-summary" role="alert" tabIndex={-1} ref={summaryRef}><strong>Something needs attention</strong><span>{message}</span></div>;
}

function focusSummary(summaryRef: React.RefObject<HTMLDivElement | null>) {
  setTimeout(() => summaryRef.current?.focus());
}

function TextField({ label, name, id = name, type = "text", placeholder, autoComplete, error, defaultValue, readOnly, minLength, maxLength }: {
  label: string; name: string; type?: string; placeholder?: string; autoComplete?: string; error?: string;
  id?: string; defaultValue?: string; readOnly?: boolean; minLength?: number; maxLength?: number;
}) {
  const errorId = error ? `${id}-error` : undefined;
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input id={id} name={name} type={type} placeholder={placeholder} autoComplete={autoComplete} defaultValue={defaultValue}
        readOnly={readOnly} minLength={minLength} maxLength={maxLength} aria-invalid={Boolean(error)} aria-describedby={errorId} required={!readOnly} />
      {error && <span className="field-error" id={errorId}>{error}</span>}
    </div>
  );
}

function Login() {
  const queryClient = useQueryClient();
  const summaryRef = useRef<HTMLDivElement>(null);
  const mutation = useMutation({
    mutationFn: (credentials: { username: string; password: string }) => api<Session>("/api/session", { method: "POST", body: JSON.stringify(credentials) }),
    onSuccess: (session) => { queryClient.setQueryData(["session"], session); navigate("/"); },
    onError: () => focusSummary(summaryRef),
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutation.mutate({ username: String(form.get("username")), password: String(form.get("password")) });
  }
  return (
    <PublicFrame eyebrow="Welcome back" title="Find your way home." intro="Sign in to return to your personal atlas of movement.">
      <form className="stack" onSubmit={submit} noValidate>
        <ErrorSummary message={mutation.isError ? SAFE_ERRORS.login : undefined} summaryRef={summaryRef} />
        <TextField label="Username" name="username" placeholder="Username or E-mail" autoComplete="username" />
        <TextField label="Password" name="password" type="password" autoComplete="current-password" />
        <div className="form-options"><AppLink to="/forgot-password">Forgot password?</AppLink></div>
        <button className="primary" disabled={mutation.isPending}>{mutation.isPending ? "Signing in..." : "Sign in"}<span aria-hidden="true">&rarr;</span></button>
      </form>
    </PublicFrame>
  );
}

function ForgotPassword() {
  const [sent, setSent] = useState(false);
  const summaryRef = useRef<HTMLDivElement>(null);
  const mutation = useMutation({
    mutationFn: (username: string) => api<void>("/api/password-reset-requests", { method: "POST", body: JSON.stringify({ username }) }),
    onSuccess: () => setSent(true),
    onError: () => focusSummary(summaryRef),
  });
  return (
    <PublicFrame eyebrow="Account recovery" title={sent ? "Check your inbox." : "Let's retrace your steps."} intro={sent ? "Your recovery request has been accepted." : "Enter your username or email. We'll send instructions if an account matches."}>
      {sent ? <div className="success-state" role="status"><span aria-hidden="true">&#10003;</span><p>If an account matches those details, a reset link is on its way.</p><AppLink to="/login" className="primary link-button">Return to sign in</AppLink></div> :
        <form className="stack" onSubmit={(event) => { event.preventDefault(); mutation.mutate(String(new FormData(event.currentTarget).get("username"))); }} noValidate>
          <ErrorSummary message={mutation.isError ? SAFE_ERRORS.recovery : undefined} summaryRef={summaryRef} />
          <TextField label="Username or E-mail" name="username" autoComplete="username" />
          <button className="primary" disabled={mutation.isPending}>{mutation.isPending ? "Sending..." : "Send reset link"}<span aria-hidden="true">&rarr;</span></button>
          <AppLink to="/login" className="quiet-link">Back to sign in</AppLink>
        </form>}
    </PublicFrame>
  );
}

function tokenFromPath(path: string, kind: "invitation" | "reset") {
  const params = new URLSearchParams(path.split("?")[1] ?? "");
  const prefix = kind === "invitation" ? "/invitations/" : "/password-resets/";
  const value = path.split("?")[0].startsWith(prefix) ? path.split("?")[0].slice(prefix.length) : params.get("token") ?? "";
  try { return decodeURIComponent(value); } catch { return ""; }
}

function Registration({ path, passwordMinimumLength }: { path: string; passwordMinimumLength: number }) {
  const token = tokenFromPath(path, "invitation");
  const summaryRef = useRef<HTMLDivElement>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const invitation = useQuery({ queryKey: ["invitation", token], queryFn: () => api<{ email: string }>(`/api/invitations/${encodeURIComponent(token)}`), enabled: Boolean(token), retry: false });
  const registration = useMutation({
    mutationFn: (body: Record<string, string>) => api<void>("/api/registrations", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => navigate("/login?registered=1"),
    onError: (error) => {
      if (error instanceof ApiError && error.problem.errors) {
        const allowed = new Set(["username", "fullName", "password", "passwordConfirmation"]);
        setErrors(Object.fromEntries(error.problem.errors.filter(({ field }) => allowed.has(field)).map(({ field, message }) => [field, message || "This value is invalid."])));
      }
      focusSummary(summaryRef);
    },
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const values = Object.fromEntries(new FormData(event.currentTarget)) as Record<string, string>;
    const next: Record<string, string> = {};
    if (values.password.length < passwordMinimumLength) next.password = `Use at least ${passwordMinimumLength} characters.`;
    if (values.password !== values.passwordConfirmation) next.passwordConfirmation = "Passwords must match.";
    setErrors(next);
    if (Object.keys(next).length) { focusSummary(summaryRef); return; }
    registration.mutate({ token, username: values.username, fullName: values.fullName, password: values.password, passwordConfirmation: values.passwordConfirmation });
  }
  return (
    <PublicFrame eyebrow="Your invitation" title="Begin your own atlas." intro="Create the private account connected to this invitation.">
      {token && invitation.isPending && <p role="status" className="loading-line">Checking invitation...</p>}
      {(!token || invitation.isError) && <><div role="alert" className="error-summary">{SAFE_ERRORS.invitation}</div><AppLink to="/login" className="quiet-link">Return to sign in</AppLink></>}
      {invitation.data && <form className="stack" onSubmit={submit} noValidate>
        <ErrorSummary message={registration.isError ? SAFE_ERRORS.registration : Object.keys(errors).length ? "Review the highlighted fields." : undefined} summaryRef={summaryRef} />
        <TextField label="E-mail" name="email" type="email" defaultValue={invitation.data.email} readOnly />
        <TextField label="Username" name="username" autoComplete="username" error={errors.username} />
        <TextField label="Full name" name="fullName" autoComplete="name" error={errors.fullName} />
        <div className="field-pair">
          <TextField label="Password" name="password" type="password" autoComplete="new-password" minLength={passwordMinimumLength} maxLength={512} error={errors.password} />
          <TextField label="Confirm password" name="passwordConfirmation" type="password" autoComplete="new-password" minLength={passwordMinimumLength} maxLength={512} error={errors.passwordConfirmation} />
        </div>
        <p className="field-hint">{passwordMinimumLength}-128 characters. Spaces and symbols are welcome.</p>
        <button className="primary" disabled={registration.isPending}>{registration.isPending ? "Creating account..." : "Create account"}<span aria-hidden="true">&rarr;</span></button>
      </form>}
    </PublicFrame>
  );
}

function ResetPassword({ path, passwordMinimumLength }: { path: string; passwordMinimumLength: number }) {
  const token = tokenFromPath(path, "reset");
  const summaryRef = useRef<HTMLDivElement>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [complete, setComplete] = useState(false);
  const mutation = useMutation({
    mutationFn: (body: Record<string, string>) => api<void>("/api/password-resets", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => setComplete(true),
    onError: () => focusSummary(summaryRef),
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const values = Object.fromEntries(new FormData(event.currentTarget)) as Record<string, string>;
    const next: Record<string, string> = {};
    if (!token) next.password = "This reset link is invalid.";
    else if (values.password.length < passwordMinimumLength) next.password = `Use at least ${passwordMinimumLength} characters.`;
    if (values.password !== values.passwordConfirmation) next.passwordConfirmation = "Passwords must match.";
    setErrors(next);
    if (Object.keys(next).length) { focusSummary(summaryRef); return; }
    mutation.mutate({ token, password: values.password, passwordConfirmation: values.passwordConfirmation });
  }
  return (
    <PublicFrame eyebrow="Secure your account" title={complete ? "Your password is reset." : "Choose a new trail key."} intro={complete ? "All earlier sessions have been signed out for your protection." : "Create a long, memorable password you don't use elsewhere."}>
      {complete ? <AppLink to="/login" className="primary link-button">Sign in again</AppLink> : <form className="stack" onSubmit={submit} noValidate>
        <ErrorSummary message={mutation.isError ? SAFE_ERRORS.recovery : Object.keys(errors).length ? "Review the highlighted fields." : undefined} summaryRef={summaryRef} />
        <TextField label="New password" name="password" type="password" autoComplete="new-password" minLength={passwordMinimumLength} maxLength={512} error={errors.password} />
        <TextField label="Confirm new password" name="passwordConfirmation" type="password" autoComplete="new-password" minLength={passwordMinimumLength} maxLength={512} error={errors.passwordConfirmation} />
        <p className="field-hint">Use {passwordMinimumLength}-128 characters.</p>
        <button className="primary" disabled={mutation.isPending}>{mutation.isPending ? "Resetting..." : "Reset password"}<span aria-hidden="true">&rarr;</span></button>
      </form>}
    </PublicFrame>
  );
}

function Modal({ open, onOpenChange, returnFocus, title, description, children, className = "" }: {
  open: boolean; onOpenChange: (open: boolean) => void; returnFocus: React.RefObject<HTMLButtonElement | null>;
  title: string; description: string; children: ReactNode; className?: string;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className={`dialog-content ${className}`} onCloseAutoFocus={(event) => { event.preventDefault(); returnFocus.current?.focus(); }}>
          <div className="dialog-heading"><div><Dialog.Title>{title}</Dialog.Title><Dialog.Description>{description}</Dialog.Description></div><Dialog.Close className="icon-button" aria-label={`Close ${title}`}>&times;</Dialog.Close></div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function PreferencesDialog({ open, onOpenChange, returnFocus, profile, preferences, csrfToken, pageSizeMaximum, onSaved }: {
  open: boolean; onOpenChange: (open: boolean) => void; returnFocus: React.RefObject<HTMLButtonElement | null>;
  profile: Profile; preferences: Preferences; csrfToken: string; pageSizeMaximum: number; onSaved: (profile: Profile, preferences: Preferences) => void;
}) {
  const summaryRef = useRef<HTMLDivElement>(null);
  const id = useId();
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const browserTimeZone = (() => {
    try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; }
    catch { return "UTC"; }
  })();
  const initialTimeZone = preferences.timezone === "UTC" && browserTimeZone !== "UTC" ? browserTimeZone : preferences.timezone;
  const timeZoneOptions = sortedTimeZoneOptions(initialTimeZone);
  const pageSizeChoices = PAGE_SIZE_CHOICES.filter((choice) => choice <= pageSizeMaximum);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError("");
    const formData = new FormData(event.currentTarget);
    const values = Object.fromEntries(formData) as Record<string, string>;
    try { new Intl.DateTimeFormat("en", { timeZone: values.timezone }).format(); } catch { setError("Enter a valid time zone, such as America/Denver."); focusSummary(summaryRef); return; }
    const pageSize = Number(values.pageSize);
    if (!pageSizeChoices.includes(pageSize)) { setError("Choose one of the available workout page sizes."); focusSummary(summaryRef); return; }
    const selectedColumns = new Set(formData.getAll("workoutColumns").map(String));
    const workoutColumns = WORKOUT_COLUMN_CHOICES.map(([value]) => value).filter((value) => selectedColumns.has(value));
    const nextProfile = { ...profile, fullName: values.fullName.trim() };
    const nextPreferences: Preferences = { theme: values.theme as Preferences["theme"], units: values.units as Preferences["units"], timezone: values.timezone, firstWeekday: values.firstWeekday as Preferences["firstWeekday"], clockFormat: values.clockFormat as Preferences["clockFormat"], workoutColumns, pageSize };
    setSaving(true);
    try {
      const savedProfile = await api<Profile>("/api/me", { method: "PATCH", body: JSON.stringify({ fullName: nextProfile.fullName }) }, csrfToken);
      const savedPreferences = await api<Preferences>("/api/me/preferences", { method: "PATCH", body: JSON.stringify(nextPreferences) }, csrfToken);
      applyTheme(savedPreferences.theme);
      onSaved(savedProfile, savedPreferences);
      onOpenChange(false);
    } catch { setError(SAFE_ERRORS.preferences); focusSummary(summaryRef); }
    finally { setSaving(false); }
  }
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content preferences-dialog" onCloseAutoFocus={(event) => { event.preventDefault(); returnFocus.current?.focus(); }}>
        <header className="dialog-heading preferences-header"><div><Dialog.Title>Preferences</Dialog.Title><Dialog.Description>Set how your routes, dates, and profile appear.</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="Close Preferences">&times;</Dialog.Close></header>
        <form onSubmit={submit} className="preferences-form" noValidate>
          <div className="preferences-body">
            <ErrorSummary message={error} summaryRef={summaryRef} />
            <fieldset><legend>Profile</legend><div className="settings-grid"><TextField label="Username" name="readonly-username" id={`${id}-username`} defaultValue={profile.username} readOnly /><TextField label="E-mail" name="readonly-email" id={`${id}-email`} defaultValue={profile.email} readOnly /><TextField label="Full name" name="fullName" id={`${id}-full-name`} defaultValue={profile.fullName} autoComplete="name" /></div></fieldset>
            <fieldset><legend>Display</legend><div className="settings-grid">
              <SelectField label="Theme" name="theme" id={`${id}-theme`} value={preferences.theme} options={["dark", "light"]} />
              <SelectField label="Units" name="units" id={`${id}-units`} value={preferences.units} options={["imperial", "metric"]} />
              <div className="field"><label htmlFor={`${id}-timezone`}>Time zone</label><select id={`${id}-timezone`} name="timezone" defaultValue={initialTimeZone}>{timeZoneOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></div>
              <SelectField label="First weekday" name="firstWeekday" id={`${id}-weekday`} value={preferences.firstWeekday} options={["monday", "sunday"]} />
              <SelectField label="Clock format" name="clockFormat" id={`${id}-clock`} value={preferences.clockFormat} options={["12h", "24h"]} />
              <div className="field"><label htmlFor={`${id}-page-size`}>Workouts per page</label><select id={`${id}-page-size`} name="pageSize" defaultValue={preferences.pageSize}>{pageSizeChoices.map((choice) => <option key={choice} value={choice}>{choice}</option>)}</select></div>
              <div className="field field--wide"><span className="field-label" id={`${id}-columns`}>Workout columns</span><div className="checkbox-list" role="group" aria-labelledby={`${id}-columns`}>{WORKOUT_COLUMN_CHOICES.map(([value, label]) => <label className="checkbox-option" key={value}><input type="checkbox" name="workoutColumns" value={value} defaultChecked={preferences.workoutColumns.includes(value)} /><span>{label}</span></label>)}</div><span className="field-hint">Choose the columns to show; they appear in this order.</span></div>
            </div></fieldset>
          </div>
          <footer className="dialog-actions"><Dialog.Close type="button" className="secondary">Cancel</Dialog.Close><button className="primary" disabled={saving}>{saving ? "Saving..." : "Save preferences"}</button></footer>
        </form>
      </Dialog.Content></Dialog.Portal>
    </Dialog.Root>
  );
}

function SelectField({ label, name, id, value, options }: { label: string; name: string; id: string; value: string; options: string[] }) {
  return <div className="field"><label htmlFor={id}>{label}</label><select id={id} name={name} defaultValue={value}>{options.map((option) => <option key={option} value={option}>{option === "12h" ? "12-hour" : option === "24h" ? "24-hour" : option[0].toUpperCase() + option.slice(1)}</option>)}</select></div>;
}

function Shell({ session, pageSizeMaximum }: { session: Session; pageSizeMaximum: number }) {
  const queryClient = useQueryClient();
  const [data, setData] = useState<{ profile: Profile; preferences: Preferences }>();
  const [loadError, setLoadError] = useState(false);
  const [avatarFailed, setAvatarFailed] = useState(false);
  useEffect(() => {
    let active = true;
    Promise.all([api<Profile>("/api/me"), api<Preferences>("/api/me/preferences")]).then(([profile, preferences]) => {
      applyTheme(preferences.theme);
      if (active) setData({ profile, preferences });
    }).catch(() => { if (active) setLoadError(true); });
    return () => { active = false; };
  }, []);
  const [dialog, setDialog] = useState<"preferences" | "about" | null>(null);
  const [menuError, setMenuError] = useState("");
  const avatarTriggerRef = useRef<HTMLButtonElement>(null);
  const signout = useMutation({
    mutationFn: () => api<void>("/api/session", { method: "DELETE" }, session.csrfToken),
    onSuccess: () => { queryClient.setQueryData(["session"], null); queryClient.removeQueries(); navigate("/login"); },
    onError: () => setMenuError("Sign out failed. Your session is still active."),
  });
  if (loadError) return <main className="center-state"><Mark /><h1>We couldn't load your explorer.</h1><p role="alert">Your session is active, but profile preferences are unavailable.</p><button className="secondary" onClick={() => location.reload()}>Try again</button></main>;
  if (!data) return <main className="center-state" aria-busy="true"><Mark /><p role="status">Restoring your route preferences...</p></main>;
  const initials = data.profile.fullName.split(/\s+/).map((part) => part[0]).join("").slice(0, 2).toUpperCase() || data.profile.username.slice(0, 2).toUpperCase();
  const switchTheme = async () => {
    const previous = data.preferences.theme;
    const theme = previous === "dark" ? "light" : "dark";
    applyTheme(theme);
    setData({ ...data, preferences: { ...data.preferences, theme } });
    try {
      const preferences = await api<Preferences>("/api/me/preferences", { method: "PATCH", body: JSON.stringify({ theme }) }, session.csrfToken);
      applyTheme(preferences.theme);
      setData((current) => current ? { ...current, preferences: { ...current.preferences, theme: preferences.theme } } : current);
    } catch {
      applyTheme(previous);
      setData((current) => current ? { ...current, preferences: { ...current.preferences, theme: previous } } : current);
      setMenuError("Theme change wasn't saved.");
    }
  };
  const avatar = <span className="avatar">{!avatarFailed && <img src="/api/me/avatar" alt="" onError={() => setAvatarFailed(true)} />}<span>{initials}</span></span>;
  return (
    <div className="shell">
      <header className="app-header">
        <a href="/" className="wordmark" aria-label="Workouts Explorer home" onClick={(event) => event.preventDefault()}><Mark compact /><span>Workouts Explorer</span></a>
        <nav aria-label="Primary"><a href="/" aria-current="page">Summary</a><span aria-disabled="true">Map</span></nav>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger ref={avatarTriggerRef} className="avatar-trigger" aria-label={`Open account menu for ${data.profile.fullName}`}>{avatar}<span className="avatar-name">{data.profile.fullName}</span><span aria-hidden="true">&#8964;</span></DropdownMenu.Trigger>
          <DropdownMenu.Portal><DropdownMenu.Content className="menu-content" align="end" sideOffset={10}>
            <DropdownMenu.Label><strong>{data.profile.fullName}</strong><span>@{data.profile.username}</span></DropdownMenu.Label>
            <DropdownMenu.Separator />
            <DropdownMenu.Item onSelect={() => setDialog("preferences")}>Preferences</DropdownMenu.Item>
            <DropdownMenu.Item onSelect={() => void switchTheme()}>Switch to {data.preferences.theme === "dark" ? "light" : "dark"} theme</DropdownMenu.Item>
            <DropdownMenu.Item onSelect={() => setDialog("about")}>About Workouts Explorer</DropdownMenu.Item>
            <DropdownMenu.Separator />
            <DropdownMenu.Item className="danger-item" disabled={signout.isPending} onSelect={() => signout.mutate()}>{signout.isPending ? "Signing out..." : "Sign out"}</DropdownMenu.Item>
          </DropdownMenu.Content></DropdownMenu.Portal>
        </DropdownMenu.Root>
      </header>
      {menuError && <div className="shell-alert" role="alert">{menuError}<button aria-label="Dismiss message" onClick={() => setMenuError("")}>&times;</button></div>}
      <PreferencesDialog open={dialog === "preferences"} onOpenChange={(open) => setDialog(open ? "preferences" : null)} returnFocus={avatarTriggerRef} profile={data.profile} preferences={data.preferences} csrfToken={session.csrfToken} pageSizeMaximum={pageSizeMaximum} onSaved={(profile, preferences) => setData((current) => current ? { profile, preferences: { ...preferences, dateRange: current.preferences.dateRange } } : { profile, preferences })} />
      <Modal open={dialog === "about"} onOpenChange={(open) => setDialog(open ? "about" : null)} returnFocus={avatarTriggerRef} title="About Workouts Explorer" description="A private atlas for a lifetime of movement.">
        <div className="about-copy"><Mark /><p>Workouts Explorer turns your personal activity history into routes you can revisit, compare, and understand without giving up ownership of the journey.</p><p className="version">Milestone 3 &middot; Workout summary</p></div>
      </Modal>
      <Summary preferences={data.preferences} csrfToken={session.csrfToken} onDateRangeSaved={(dateRange) => setData((current) => current ? { ...current, preferences: { ...current.preferences, dateRange } } : current)} />
    </div>
  );
}

function isPublicPath(path: string) {
  const pathname = path.split("?")[0];
  return pathname === "/login" || pathname === "/forgot-password" || pathname === "/reset-password" || pathname === "/register" || pathname.startsWith("/password-resets/") || pathname.startsWith("/invitations/");
}

export function App() {
  const path = usePathname();
  const queryClient = useQueryClient();
  const expirationHandled = useRef(false);
  const session = useQuery({ queryKey: ["session"], queryFn: () => api<Session>("/api/session"), retry: false });
  const publicConfig = useQuery({ queryKey: ["public-config"], queryFn: loadPublicConfig, retry: false });
  useEffect(() => {
    if (session.data) expirationHandled.current = false;
  }, [session.data]);
  useEffect(() => {
    let active = true;
    const expireSession = () => {
      if (expirationHandled.current) return;
      expirationHandled.current = true;
      const sessionQuery = (query: { queryKey: readonly unknown[] }) => query.queryKey[0] !== "public-config";
      const authenticatedQuery = (query: { queryKey: readonly unknown[] }) => sessionQuery(query) && query.queryKey[0] !== "session";
      void (async () => {
        await queryClient.cancelQueries({ predicate: sessionQuery });
        if (!active) return;
        queryClient.removeQueries({ predicate: authenticatedQuery });
        queryClient.setQueryData(["session"], null);
        navigate("/login", true);
      })();
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, expireSession);
    return () => { active = false; window.removeEventListener(SESSION_EXPIRED_EVENT, expireSession); };
  }, [queryClient]);
  const config = publicConfig.data ?? LOADING_CONFIG;
  if (publicConfig.isPending) return <main className="center-state" aria-busy="true"><Mark /><p role="status">Loading application settings...</p></main>;
  if (publicConfig.isError) return <main className="center-state"><Mark /><h1>Workouts Explorer is unavailable.</h1><p role="alert">Application settings could not be loaded. Please try again later.</p></main>;
  const pathname = path.split("?")[0];
  const tokenFlow = pathname === "/reset-password" || pathname === "/register" || pathname.startsWith("/password-resets/") || pathname.startsWith("/invitations/");
  if (isPublicPath(path) && (tokenFlow || !session.data)) {
    if (path.startsWith("/forgot-password")) return <ForgotPassword />;
    if (path.startsWith("/reset-password") || path.startsWith("/password-resets/")) return <ResetPassword path={path} passwordMinimumLength={config.passwordMinimumLength} />;
    if (path.startsWith("/register") || path.startsWith("/invitations/")) return <Registration path={path} passwordMinimumLength={config.passwordMinimumLength} />;
    return <Login />;
  }
  if (session.isPending) return <main className="center-state" aria-busy="true"><Mark /><p role="status">Opening your explorer...</p></main>;
  if (session.data) return <Shell session={session.data} pageSizeMaximum={config.pageSizeMaximum} />;
  return <Login />;
}
