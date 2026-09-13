// Agent UI bundle.
//
// Runs inside the Tauri shell and calls rfm-core directly — never over a socket (r3 §7). Screens land in #35–#38.
export const PACKAGE = "ui" as const;
