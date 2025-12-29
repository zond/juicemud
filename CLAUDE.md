# CLAUDE.md

Instructions for Claude Code when working with this repository.

## Rules

- CLAUDE.md should only contain AI instructions, not general project documentation,
  and README.md should contain project overview, architecture, and usage instructions.
  This will ensure that we keep documentation separate in logical units.
- docs/ should contain detailed documentation for specific subsystems that need extra insight.
  This will ensure we keep a live understanding of how the system is supposed to work, and that new readers have
  a way to learn.
- Always update CLAUDE.md, README.md, and docs/ when they become out of date.
  This will ensure that the documentation is relevant.
- Always make sure functions have doc comments, if they are too complex for their name to fully describe what they do.
  This will make sure readers and authors of our code know what they functions are supposed to do, and have an
  easier time learning about them.
- Always ask your agent to review each new git commit.
- Always make sure the README is up to date with relevant new information, and doesn't contain redundant or outdated
  information.
- Always test functionality if reasonably possible.

### Integration tests
- Integration tests should use the SSH interfaces for all interactions with the server, except when it's unreasonably
  difficult or messy not to, in which case they are allowed to run direct function calls on the test client
  or test server objects. This will ensure that we test the ways users and wizards interact with the game.
- Integration tests should avoid sleeping fixed times to wait for events to occur, instead they should wait to be notified,
  or if that's impossible, loop around a short sleep/check block that polls. This will ensure the integration tests don't
  become slower than they need to.

### Go MCP server
- If there is no gopls MCP server, run `claude mcp add gopls -- gopls mcp` to install it.
- Run `gopls mcp -instructions` for instructions about how to interact with the gopls MCP server.
- Always fix hints provided by gopls.
