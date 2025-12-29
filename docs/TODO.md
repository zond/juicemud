# TODO

Known issues and tasks to address.

## Research

- [ ] Benchmark JS engines with Go bindings to find the one that fits our usecase the best

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
  - Emits to objects in the specified location with `Perspective: {Here: true}`
  - Also emits to neighbouring rooms via exits with `Perspective: {Exit: exitObject}`
  - Exit's TransmitChallenges filter which observers can perceive through that exit
  - Regular challenges parameter still filters individual recipients
  - Exit is from observer's perspective (exit back to source location)

- [x] Add JS function to print to connection
  - `print(message)` outputs directly to the object's SSH connection if one exists
  - Silently does nothing for objects without connections (NPCs, etc.)
  - Use for immediate output that doesn't need to go through the event queue
  - Example: In `user.js`: `addCallback('smell', ['emit'], (event) => print(event.description))`

### Debug System

- [x] Add console.log ring buffer for /debug
  - 64-message buffer per object stores recent console.log output
  - When /debug connects, buffered messages are dumped first
  - Helps debug objects that have already errored or run intermittently
