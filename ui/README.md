# GoBale administration UI

The panel is part of the GoBale binary, served at `<APP_BASE_PATH>/ui/`. It manages connections, native Bale sign-in, webhook settings and delivery diagnostics. It deliberately has no messaging UI, send tester, operator accounts or API explorer.

## Development

Use Node.js 24.12+ and the committed lockfile. First build and start GoBale as
described in the root [README](../readme.md#build-from-source), using a separate
development configuration and storage directory. Keep `APP_UI_ENABLED=true`,
`APP_BASE_PATH` empty and `APP_UI_PUBLIC_ORIGIN` unset for the local Vite proxy.
Then, in another terminal at the repository root:

```sh
cd ui
npm ci
npm run dev -- --port 15173
```

Open `http://127.0.0.1:15173/ui/`. Vite proxies `/ui/auth` and `/ui/api` to `http://127.0.0.1:3000`; set `GOBALE_DEV_PROXY` to change the development backend. No cross-origin API mode is implemented. The browser only talks to its own origin.
The proxy preserves the browser Host and Origin so the normal server checks apply.
Sign in with that development server's `APP_BASIC_AUTH`; actions affect its storage
and connected accounts. Prefer the isolated E2E runner below when testing account
lifecycle failures. `npm run preview` alone has no administrative backend proxy.

`npm run build` performs type checking, builds local assets in `src/ui/web/dist`, collects runtime dependency/font notices, and writes the version/contract/file-digest manifest used by GoBale's embedded asset validation. The official root build commands run this automatically. Generated assets and browser outputs are ignored. Node is not required to run the resulting GoBale binary.

## Verification

```sh
npm run typecheck
npm run lint
npm test
npm run build
npx playwright install chromium firefox webkit
npm run e2e
```

The root browser-test runner creates an isolated Go server with temporary SQLite storage, synthetic accounts and a non-root `/gateway` base path. No native provider calls or outbound webhook workers run. All three browser engines exercise the embedded build; fixtures test failure/race paths, and the real Go subset checks cookies, CSRF, account creation, login, webhook configuration and delivery reads. The suite also checks English/Persian, RTL, mobile, dark mode, keyboard dialog focus, WCAG AA checks and a 50-account snapshot. Screenshots/traces contain synthetic data and remain in ignored `test-results/`.

## Internal boundaries

- `lib/api.ts` owns same-origin fetch, the three-request gate, session generation and explicit immutable account headers. It does not keep a global selected account.
- Query keys contain `instance_id`. Account replacement and session expiry clear private cached data. Each form cancels queued work when unmounted; this does not undo a write the server already accepted.
- Only language and theme are persisted in browser storage. Administrative credentials, Bale login values and webhook secrets remain in component/request memory.
- POST/PATCH/DELETE calls are never automatically retried. `UI_UNAUTHORIZED` ends the panel session; provider authentication errors remain account-scoped.
- HashRouter preserves deployment base paths. All scripts, CSS and fonts are embedded; there is no CDN, service worker or downloaded UI at runtime.
- `licenses/README.md` documents the one upstream license omitted from a dependency's npm tarball. Other notices are collected from locked installed packages.
