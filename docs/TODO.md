# TODO

Known issues and tasks to address.

## Features

### Wizard Commands

- [x] Add wizard command to send generic events
  - `/emit <target> <eventName> <tag> <message>` - sends event to object
  - Tags: emit, command, action
  - Message must be valid JSON

### Event System

- [x] Handle 'message' events in handleEmitEvent
  - When an object receives a 'message' event with `{"text": "..."}`, prints to connected terminal
  - Useful for NPC dialogue, system messages, etc.

- [x] Extend `emitToLocation` to emit to neighbourhood
  - Emits to objects in the specified location with `source: {here: true}`
  - Also emits to neighbouring rooms via exits with `source: {exit: "exitName"}`
  - Exit's TransmitChallenges filter which observers can perceive through that exit
  - Regular challenges parameter still filters individual recipients
  - Exit name is from observer's perspective (exit back to source location)

- [ ] Add JS function to print to connection
  - Add `printToConnection(message)` JS function available on objects with active connections
  - Allows wizards to configure how sensory events are rendered
  - Example: In `user.js`: `addCallback('smell', ['emit'], (event) => printToConnection(event.description))`
  - Keeps sensory event definitions flexible - wizards decide what senses/skills exist
