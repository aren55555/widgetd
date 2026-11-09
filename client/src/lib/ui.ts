// Tailwind class lists shared by the admin pages and the settings form.
// Tailwind finds classes by scanning these files for literal tokens, so
// every class must appear spelled out here (no string building of names).

export const input =
  "mt-1 block w-full rounded-lg border border-gray-500/25 bg-transparent p-2.5 aria-invalid:border-red-600 disabled:opacity-60";
export const select = input;

const btnBase = "cursor-pointer rounded-[10px] font-semibold disabled:cursor-default disabled:opacity-60";
export const btn = {
  /** The page's main action: full width. */
  primary: `${btnBase} block w-full bg-blue-600 p-3 text-white`,
  small: `${btnBase} bg-blue-600 px-3 py-1.5 text-sm text-white`,
  danger: `${btnBase} bg-red-600 px-3 py-1.5 text-sm text-white`,
  secondary: `${btnBase} bg-gray-500/20 px-3 py-1.5 text-sm`,
};

export const muted = "text-sm opacity-65";
export const error = "text-red-600";
export const status = "text-sm empty:hidden";
export const fieldError = "mt-1 text-sm text-red-600 empty:hidden";
export const label = "block text-sm font-semibold";
export const field = "my-4 block";
export const check = "my-2 flex items-center gap-2";
export const fieldset = "my-4 rounded-[10px] border border-gray-500/25 p-3";
export const legend = "px-1 font-semibold";
export const link = "text-blue-600 underline dark:text-blue-400";
export const code = "my-2 text-center font-mono text-[2rem] leading-tight font-semibold tracking-widest";
export const list = "border-t border-gray-500/20";
export const item = "flex flex-col items-start gap-2 border-b border-gray-500/20 py-3";
