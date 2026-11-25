// Package emailstub holds the one stub of Cloudflare's email API that
// tailworks ships (email is not supported, docs/COMPAT.md "Email"), and the
// bundler's refusal of the app's own imports (refuse.go). Where the stub
// applies, importing `cloudflare:email` works; constructing an EmailMessage
// throws.
//
// Two places use the stub:
//   - the built-in bundler (internal/esbundle) resolves a dependency's
//     import of `cloudflare:email` to ClassJS, as
//     js/packages/create-tailworks/lib/build.mjs does, and refuses the
//     app's own import (refuse.go);
//   - the generated workerd config (hostd/gen, email.go) provides
//     InternalModuleJS as the internal module `cloudflare-internal:email`
//     that stock workerd's own `cloudflare:email` imports, and ClassJS as
//     `cloudflare:email` in the facets' Dynamic Worker, so a Worker that
//     tailworks did not bundle (a pre-built `no_bundle` or Vite-built
//     Worker, issue #354) can import `cloudflare:email` too.
package emailstub

// Message is the error an EmailMessage constructor throws.
const Message = "cloudflare:email is not supported on tailworks"

// ClassJS is the stub module the bundler substitutes for `cloudflare:email`.
// build.mjs carries the same text (TestBuildMJSMatches).
const ClassJS = "export class EmailMessage { constructor() { throw new Error('" + Message + "'); } }"

// InternalModule is the name stock workerd's `cloudflare:email` imports
// (`import { default as email } from 'cloudflare-internal:email'`, then
// `export const { EmailMessage } = email`). Production workerd builds it in;
// stock workerd does not.
const InternalModule = "cloudflare-internal:email"

// InternalModuleJS is InternalModule's source: ClassJS, plus the default
// export that workerd's `cloudflare:email` reads EmailMessage from.
const InternalModuleJS = ClassJS + "\nexport default { EmailMessage };\n"
