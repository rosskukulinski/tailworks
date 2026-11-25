// Only Vite can build this (the virtual module); tw must not try.
import build from "virtual:react-router/server-build";
export default { fetch: () => new Response(String(build)) };
