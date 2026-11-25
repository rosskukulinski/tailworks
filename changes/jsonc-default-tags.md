---
kind: feature
upgrade: [protocol]
---
`tailworks.jsonc` `tailnet.tags` is now optional: when it's left out,
`tw deploy` and `tw dev` tag the workload node `tag:tw-wl-<workload>`, the
same default a project without a `tailworks.jsonc` already gets, so a
minimal `tailworks.jsonc` is `{}`. The default is written into the bundled
`tailworks.jsonc`, so mgmt checks it like a tag you wrote (syntax, reserved
tags, `tagOwners`). An explicit `"tags": []` is still an error. With
`--env`, an omitted `tags` follows the environment's workload name
(`tag:tw-wl-shop-staging`). The `hello`, `static-site` and `python`
templates no longer list the tag, `tw init --tags` adds the line when the
template has none, and the `ai-agent` template's workload tag is now
`tag:tw-wl-ai-agent` (was `tag:tw-wl-ai`), matching its name.

`tailnet.objects.<Class>.agent` is no longer read: it only filled the
"Agent" column of the mgmt UI's deployment page, which is gone, and the
`agent` field is dropped from the API's `ClassConfig` (mgmt ignores it if
an older `tw` still sends it). The schema still accepts the key, so
existing files keep deploying.
