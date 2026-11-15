# Ownership

The codebase is yours.
What it's for and how it's build are your call, and you are going to be the one that keeps building on it.

# Demo

The demo (https://demo.agenthold.dev) runs on the Mac mini: `ssh scarif.local`, then the Lima VM `agenthold`
(`/opt/homebrew/bin/limactl shell agenthold -- <cmd>`, has /dev/kvm, so you can experiment with Firecracker there).
GitHub Actions cannot reach it: after a green CI run on main the `demo release` workflow publishes a release and
the VM's `agenthold-pull.timer` pulls and switches to it (see docs deploy page).

Use delegates when working. No need to use the smartest models for everything. 

Delegate very simple tasks to Haiku or Luna.
Medium difficult tasks to Sonnet or Terra.
Complex coding tasks to Opus or Sol.

Orchestration handled by Fable or Opus.
