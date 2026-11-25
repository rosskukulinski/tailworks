---
kind: fix
upgrade: []
---
The `node-workflow` template now builds from an installed release. Before,
`tw dev` and `tw deploy` failed with `Cannot find package 'esbuild'`
because create-tailworks loaded esbuild from its own install directory,
which a release doesn't include. It now uses the app's own esbuild, and the
template lists esbuild as a dependency.

Operator action: in an app made from the `node-workflow` template with an
earlier release, run `npm install --save-dev esbuild@0.28.2`.
