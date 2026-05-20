## [1.9.1](https://github.com/Leicas/matrimail/compare/v1.9.0...v1.9.1) (2026-05-20)


### Bug Fixes

* **email:** apply Re:/quote on thread-tail replies; strip i18n + HTML quotes inbound ([85db579](https://github.com/Leicas/matrimail/commit/85db57965760fdcbdb71ddd8a6a4f53f784387ff))

## [1.9.0](https://github.com/Leicas/matrimail/compare/v1.8.0...v1.9.0) (2026-05-20)


### Features

* **email:** strip quoted history, Gmail-style replies, marketing-HTML attach, sidecar dedup ([2372e6d](https://github.com/Leicas/matrimail/commit/2372e6dabc190be736eda6e56047b5d17ceec26f))

## [1.8.0](https://github.com/Leicas/matrimail/compare/v1.7.0...v1.8.0) (2026-05-13)


### Features

* **outbound:** reply-all by default, alias-preserving From, DM toggle, thread fallback ([6cb0a89](https://github.com/Leicas/matrimail/commit/6cb0a89c4d40b7f62ec391070bae8173582135fd))

## [1.7.0](https://github.com/Leicas/matrimail/compare/v1.6.1...v1.7.0) (2026-05-12)


### Features

* **draft:** post Gmail draft body into the portal room ([ade5141](https://github.com/Leicas/matrimail/commit/ade514136f4dc519f6358db3ab07ea2b7af3cac9))

## [1.6.1](https://github.com/Leicas/matrimail/compare/v1.6.0...v1.6.1) (2026-05-12)


### Bug Fixes

* **commands:** backfill draft payload from ThreadManager + portal ID ([3c9ebd8](https://github.com/Leicas/matrimail/commit/3c9ebd89059401380492152ab795c993caae8722))

## [1.6.0](https://github.com/Leicas/matrimail/compare/v1.5.0...v1.6.0) (2026-05-12)


### Features

* **commands:** !matrimail draft fires configured webhook ([e7ccf30](https://github.com/Leicas/matrimail/commit/e7ccf3088378c727e73d9092373c2c877953f0af))

## [1.5.0](https://github.com/Leicas/matrimail/compare/v1.4.1...v1.5.0) (2026-05-12)


### Features

* **gmail-api:** backlog command + correct bridge state for modify-mode ([408c8d6](https://github.com/Leicas/matrimail/commit/408c8d69795b9edf3e287165fa2d6839fd5a2dbc))

## [1.4.1](https://github.com/Leicas/matrimail/compare/v1.4.0...v1.4.1) (2026-05-12)


### Bug Fixes

* **gmail:** include labelAdded in history poller types ([95493cf](https://github.com/Leicas/matrimail/commit/95493cf0cbab153b59477cc3c2785f7c35741d9c))

## [1.4.0](https://github.com/Leicas/matrimail/compare/v1.3.0...v1.4.0) (2026-05-11)


### Features

* multi-account login, inline images, draft filtering ([8ffddfe](https://github.com/Leicas/matrimail/commit/8ffddfe82fc3047a748d5ae81e519f697afa6b04))

## [1.3.0](https://github.com/Leicas/matrimail/compare/v1.2.2...v1.3.0) (2026-05-08)


### Features

* **matrix:** auto-tag matrimail portals as m.lowpriority ([d6b8d8f](https://github.com/Leicas/matrimail/commit/d6b8d8fd7bbd8fd238918059823ebc339b2c4067))

## [1.2.2](https://github.com/Leicas/matrimail/compare/v1.2.1...v1.2.2) (2026-05-08)


### Bug Fixes

* **threading:** preserve thread participants across degenerate emails ([5a4773c](https://github.com/Leicas/matrimail/commit/5a4773c4ab8ad5f8fd9bf4797d03b961315b9a64))

## [1.2.1](https://github.com/Leicas/matrimail/compare/v1.2.0...v1.2.1) (2026-05-08)


### Bug Fixes

* **crypto:** persist passphrase under ./data/ so it survives restart ([4b0cb53](https://github.com/Leicas/matrimail/commit/4b0cb5346b17266e4757a72040db548534a8e5bd))

## [1.2.0](https://github.com/Leicas/matrimail/compare/v1.1.3...v1.2.0) (2026-05-08)


### Features

* **outbound:** append Gmail-side signature on new threads ([acd8e61](https://github.com/Leicas/matrimail/commit/acd8e61a764cba81457c57aef09f9498dac2888d))

## [1.1.3](https://github.com/Leicas/matrimail/compare/v1.1.2...v1.1.3) (2026-05-08)


### Bug Fixes

* **db:** load auth_type in GetAccount + GetUserAccounts(Basic) ([030a434](https://github.com/Leicas/matrimail/commit/030a434f1c8f0209c7668456b3c142b970db476b))

## [1.1.2](https://github.com/Leicas/matrimail/compare/v1.1.1...v1.1.2) (2026-05-08)


### Bug Fixes

* **oauth:** IsLoggedIn returns true for modify-scope Gmail accounts ([6081b07](https://github.com/Leicas/matrimail/commit/6081b070675fad6c404adcca5deff5654c0a4fe5))

## [1.1.1](https://github.com/Leicas/matrimail/compare/v1.1.0...v1.1.1) (2026-05-08)


### Bug Fixes

* **db:** widen oauth + dedup timestamps to BIGINT for Postgres ([5d32bce](https://github.com/Leicas/matrimail/commit/5d32bcece5f1dd870d143ff4421d0e103a1159c4))

## [1.1.0](https://github.com/Leicas/matrimail/compare/v1.0.1...v1.1.0) (2026-05-08)


### Features

* **oauth:** add paste-code command for fully-headless deploys ([66ba244](https://github.com/Leicas/matrimail/commit/66ba24464c58f83fcfb04239376a49a994d59ba5))

## [1.0.1](https://github.com/Leicas/matrimail/compare/v1.0.0...v1.0.1) (2026-05-07)


### Bug Fixes

* **ci:** lowercase image name before passing to Docker tags ([b289dca](https://github.com/Leicas/matrimail/commit/b289dcac7b20afa476ed0aa89547bda791080698))

## 1.0.0 (2026-05-07)


### Features

* add iCloud email provider support ([b109e36](https://github.com/Leicas/matrimail/commit/b109e36d682e61e0ff379b47193bef3abe616c21))
* clarify folder selection message to include labels ([2158faa](https://github.com/Leicas/matrimail/commit/2158faa37a3e84af5c9e8f93db7dd72976f2e690))
* detect Google Workspace custom domains via MX lookup ([25629d9](https://github.com/Leicas/matrimail/commit/25629d9ceae554d7193171b149c321ab08910444))
* implement centralized state coordinator with standardized error handling & remove unused parseKeyString function and encoding/hex import ([2d13f3f](https://github.com/Leicas/matrimail/commit/2d13f3f91c50c7a9b57436b7fc3b9d0578922467))
* implement multi-step login flow with IMAP folder selection and validation ([a61d837](https://github.com/Leicas/matrimail/commit/a61d8375c5dddc233383e5c9c4cf3677141a813e))
* implemented the circuit breaker + state coordinator integration ([66eddbb](https://github.com/Leicas/matrimail/commit/66eddbb46cbd4aaea717c15366e697c99c1eb153))
* **matrimail:** compose flow + draft persistence + UX polish + docs (Phase D) ([6b4d5ad](https://github.com/Leicas/matrimail/commit/6b4d5adbca65e316157e02936641304712937734))
* **matrimail:** MIME builder, sent-folder dedup, Gmail OAuth login (Phase B) ([4385233](https://github.com/Leicas/matrimail/commit/438523337995b39895c46f3669bb1711b8eec30e))
* **matrimail:** Sender abstraction + Gmail API + OAuth + SMTP fallback (Phase A) ([81fe9ae](https://github.com/Leicas/matrimail/commit/81fe9ae6c9cce0a7f08b66e35eacde5159115b71))
* **matrimail:** wire outbound — RoomFeatures, HandleMatrixMessage, AppendToSent (Phase C) ([5551680](https://github.com/Leicas/matrimail/commit/5551680181509d22d382510238030740a5cbd3e4))
* **oauth:** replace device-code with auth-code + PKCE + loopback ([e6f2c39](https://github.com/Leicas/matrimail/commit/e6f2c397133a67eeeac9576f76b964cf0cf5a435))


### Bug Fixes

* Accurately report email conversion panics while preserving user experience, mask email usernames in logs based on sanitization setting for privacy, and fixed goroutine leak in IMAP auth timeout by removing unnecessary drain logic ([6e68fbb](https://github.com/Leicas/matrimail/commit/6e68fbb3606c5bb11557b25f5d40a580ea5461a1))
* add failure tracking and logging in IMAP backfill and sync to avoid silent errors ([767ad9b](https://github.com/Leicas/matrimail/commit/767ad9be167e290bd4149df38ec8d855078c0afe))
* added memory safety and operational stability in email processing ([8aaa230](https://github.com/Leicas/matrimail/commit/8aaa230556069bd2227a1fb414628fc6b765e9db))
* addressed security vulnerabilities in logging and timeout utilities ([0cfbf78](https://github.com/Leicas/matrimail/commit/0cfbf78728128c268e986bac646dcafac8ebe3c0))
* **checkDBWritable:** inline value to avoid SQLite-vs-Postgres placeholder syntax ([b08d2f5](https://github.com/Leicas/matrimail/commit/b08d2f5384b5a60dfbd562ffc359c4bae47b6d1a))
* **ci:** pin semantic-release/release-notes-generator@13 ([ac2289b](https://github.com/Leicas/matrimail/commit/ac2289b51b656c28461325a4607a900f47450725))
* circuit breaker auto-recovery after network failures ([5d5e790](https://github.com/Leicas/matrimail/commit/5d5e79092b10e535812d7ccf7f7a273cb833f032))
* **ci:** run semantic-release directly to bypass action's stale dep bundle ([b798895](https://github.com/Leicas/matrimail/commit/b7988957511d024200948b9b12647b8786a3a855))
* complete email provider coverage and add user-friendly login feedback ([24be1c1](https://github.com/Leicas/matrimail/commit/24be1c19ee2a51bc47a97c2698aacefa6bb95033))
* **db:** make all matrimail queries portable across SQLite and Postgres ([253a0dc](https://github.com/Leicas/matrimail/commit/253a0dcb5b19c46971bd173283851f4a62424243))
* decode quoted‑printable/base64 for single‑part emails so HTML renders correctly ([76d0488](https://github.com/Leicas/matrimail/commit/76d0488de7c99541533627ae3bc5805f287eb8a3))
* **docker:** allow libolm symlinks in arch-agnostic runtime stage ([89e1fa3](https://github.com/Leicas/matrimail/commit/89e1fa31acdec8f50c6edef71e9cbcb271fb43ae))
* **Dockerfile:** bump golang base to 1.25 to match go.mod ([2c6912f](https://github.com/Leicas/matrimail/commit/2c6912f216cadab50d2224b809716e8187f3303f))
* **Dockerfile:** drop unversioned libolm.so copy ([2c64583](https://github.com/Leicas/matrimail/commit/2c64583125a485de28099935a032b948c60357a5))
* **docker:** make libolm copy arch-agnostic for multi-arch builds ([7902ae5](https://github.com/Leicas/matrimail/commit/7902ae5f7778f57d6d49708f0136530788ed552a))
* EMAILDAWG_PASSPHRASE complexity barrier by implementing progressive security with ([5e864a1](https://github.com/Leicas/matrimail/commit/5e864a11bbe19b96e274d9f1c54698002892341f))
* filter out tiny placeholder images from email attachments ([03d8636](https://github.com/Leicas/matrimail/commit/03d8636b98cb33d64aaedb6a6ba06ca8dbc5f9bd))
* Fix goroutine leak in fnSync timeout handling, addressed race condition in fnReconnect, added context handling in fnSync to use command context, and input validation in processTextLogin ([9fced50](https://github.com/Leicas/matrimail/commit/9fced50fa2502623da99d8900718c2e15cf3e297))
* fix read-only rooms by blocking user messages at power level 101 ([87ad35f](https://github.com/Leicas/matrimail/commit/87ad35fa37d86d1aa2061fafe8b3b8576285f6ce))
* improve robustness and safety in login process ([8e137de](https://github.com/Leicas/matrimail/commit/8e137ded40cf0632183fe0c22a70a7e64ed896bc))
* **init:** stop wiping the YAML-decoded Config in EmailConnector.Init ([03a7b1d](https://github.com/Leicas/matrimail/commit/03a7b1d3b18ee5dd8a945ee1bdbf413e02a23476))
* **login:** don't smash OAuth instructions with app-password help text ([63fe57d](https://github.com/Leicas/matrimail/commit/63fe57d3368dfb9bc7d17b63de68c33b2215d763))
* **login:** wire interactive multi-step login through CommandState ([67392b8](https://github.com/Leicas/matrimail/commit/67392b83c47f0acf918e360f1bacd492bafe75b0))
* logout bug ([a6e6910](https://github.com/Leicas/matrimail/commit/a6e691018b8f3f5422280e9ce499729a12f9ceb3))
* Move magic numbers to constants & defined event constants in coordinator ([01097c4](https://github.com/Leicas/matrimail/commit/01097c43eb82b2f724a3aef1a57cb77e97cb98c8))
* **oauth:** use int64 for attachment Size and LoginDisplayTypeCode ([1c00b07](https://github.com/Leicas/matrimail/commit/1c00b07e3aa8a3f3d6fae0fbb0b343757ad2264b))
* potential race condition and deadlock potential in circuit breaker ([eaf5e9b](https://github.com/Leicas/matrimail/commit/eaf5e9baf4786cf7be1a632269c46178a6ef1260))
* prevent emails from being marked as read when processing ([980f54e](https://github.com/Leicas/matrimail/commit/980f54ef9b5f1e6af10e6c242d77f280944f8524))
* prevent goroutine leaks on context timeout by draining done channel in sync operation ([b7b1d0b](https://github.com/Leicas/matrimail/commit/b7b1d0bd3ce25854df942841a821d53b880ee3ac))
* remove invisible Unicode characters from email HTML content ([896ec18](https://github.com/Leicas/matrimail/commit/896ec182d6650aa26c86577b32b22297c7828468))
* remove unused parameters and methods ([6cba87d](https://github.com/Leicas/matrimail/commit/6cba87d7d3b7e82f1e5758ac000754e1d2db2e2c))
* replaced predictable math/rand jitter with crypto/rand to prevent timing attack ([bbddd79](https://github.com/Leicas/matrimail/commit/bbddd79972e816b85e89c40118829c81df4d40de))
* resilience pattern failures: remove bypasses, coordinate timeouts, improve error classification ([f5d5380](https://github.com/Leicas/matrimail/commit/f5d538022afda24cfb3f05d7f5cfdeb21806a256))
* resolve critical race conditions and architectural issues ([56ce330](https://github.com/Leicas/matrimail/commit/56ce3301da134e86437b12a14a9ca79d4195b6c8))
* resolve critical race conditions and state issues in EmailClient ([3c9ee7a](https://github.com/Leicas/matrimail/commit/3c9ee7a32c650f0de4c128752fb04d293f70ac9f))
* resolve multiple critical architectural and safety issues ([57473f9](https://github.com/Leicas/matrimail/commit/57473f95968702b73394c954ee1b9f23ccd9b867))
* resolve race conditions causing bridge crashes ([cec42ad](https://github.com/Leicas/matrimail/commit/cec42adaeac6a27849254243320ef01ea5adae67))


### Performance Improvements

* optimize database thread resolver with UNION query and better monitoring ([5396605](https://github.com/Leicas/matrimail/commit/5396605bc19377cd4a232de308081b9acfef93d6))
