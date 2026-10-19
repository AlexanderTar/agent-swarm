# Running Swarm in isolation

Use this for any e2e, probe or experiment that runs the `swarm` binary with its own data folder, and for any Go test that could reach `swarm install`, `swarm uninstall` or `launchctl`.

## Why a scratch `SWARM_HOME` is not enough

`SWARM_HOME` (or `--home`) moves Swarm's data. It does not move the machine-wide things Swarm also owns:

- the launchd labels `dev.swarm.daemon` and `dev.swarm.updater` (`internal/install/launchd.go` `Label`, `internal/install/legacy.go` `UpdaterLabel`): they are fixed, so `launchctl bootout gui/<uid>/dev.swarm.daemon` from a scratch run stops the **live** daemon, whatever `HOME` is;
- the agents' own configuration (`~/.claude`, `~/.codex`, `~/.cursor`, `~/.gemini`, `~/.grok`, muse) and `~/.local/bin/swarm`;
- `~/.swarm/research` (agents write research notes there regardless of `SWARM_HOME`).

BUG-100: an isolated e2e that ran `SWARM_HOME=<scratch> swarm install` would have booted the live daemon out.

## What the code does now

When the swarm home is not `~/.swarm`, `swarm install` and `swarm uninstall` set `install.Config.Isolated` and then:

- write no plist and run no `launchctl` command (`install.Install`, `install.Uninstall`, `install.RemoveLegacyShared`);
- touch no agent configuration, plugin, skill link or `~/.local/bin/swarm` (`install.Agents`, `install.Uninstall`);
- say so and name the flag that opts in.

`--live` opts in (`swarm install --live`, `swarm uninstall --live`): it acts on the live launchd job and the real configuration even though the home is not `~/.swarm`. Only use it with a fake `HOME` **and** a `launchctl` stub on `PATH`. The default home is unchanged.

## Safe setup for a real binary (e2e, probes)

```sh
S=$(mktemp -d)                       # keep it short: unix socket paths have a length limit
mkdir -p "$S/shim" "$S/home" "$S/fakehome"
cat >"$S/shim/launchctl" <<EOF2
#!/bin/sh
echo "launchctl \$*" >> $S/launchctl-shim.log
EOF2
chmod +x "$S/shim/launchctl"
env -i PATH="$S/shim:/usr/bin:/bin:/opt/homebrew/bin" HOME="$S/fakehome" USER=tester \
  SWARM_HOME="$S/home" SWARM_TMUX_SOCKET=swarm-e2e-mine swarm install
```

- `HOME` is a fake folder, `PATH` starts with the `launchctl` stub, and `SWARM_HOME` is scratch. Do not set `SWARM_USAGE=live`.
- After the run, `$S/launchctl-shim.log` must not exist (an isolated run never calls `launchctl`). If you pass `--live`, it lists exactly what would have hit the live job.
- Never use `--live` without the fake `HOME` and the stub.
- An agent started inside the run can still guess the real home from its work-dir path and write to `~/.swarm/research`; check the live folder afterwards and remove strays.
- `scripts/e2e.sh` follows this rule for the fake adapter: it never runs `swarm install`.

## Safe setup for Go tests

- Never call the real `install.Install`, `install.Uninstall`, `install.Agents` or `migrate` with `execx.Run` in a test. Inject `execx.Fake` (`internal/install/isolated_test.go`) and a fake `HOME` (`fakeHome` in `internal/install/codex_test.go`). In `cmd/swarm` tests, stub `installLaunchd`, `installAgents` and `installUninstall`.
- `execx.Run`, `execx.RunFor` and `execx.RunEnv` return an error for `launchctl` when running under `go test` (`refuseUnderTest`, guarded by `TestRealRunnersRefuseLaunchctlUnderGoTest`). A test that hits it forgot to inject a fake; fix the test, do not weaken the guard.
- If a test run might have touched launchd, check at once: `launchctl print gui/$(id -u)/dev.swarm.daemon | grep -E 'state|pid'`. To restore it: `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.swarm.daemon.plist`.

## Guards

| Rule | Test |
| --- | --- |
| A non-default home never reaches launchd | `TestIsolatedInstallNeverTouchesLaunchd`, `TestIsolatedUninstallNeverTouchesLaunchd`, `TestIsolatedAgentsNeverBootsOutTheUpdater` (`internal/install`) |
| A non-default home never touches agent configs | `TestIsolatedInstallAndUninstallLeaveUserAgentConfigsAlone` |
| The default home is unchanged | `TestNonIsolatedInstallStillTouchesLaunchd`, `TestInstallAndUninstallIsolateANonDefaultHome` (`cmd/swarm`) |
| Tests cannot run the real `launchctl` | `TestRealRunnersRefuseLaunchctlUnderGoTest` (`internal/execx`) |
