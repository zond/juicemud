# TODO

Known issues and tasks to address.

## Features

### Wizard Commands

- [ ] Add wizard command to send generic events
  - Should allow sending arbitrary event types to objects
  - Example: `/emit <objectID> message "Hello world"` to send a message event

### Event System

- [ ] Handle 'message' events in handleEmitEvent
  - When an object receives a 'message' event, print the message to connected player terminals
  - Useful for NPC dialogue, system messages, etc.

- [ ] Extend `emitToLocation` to emit to neighbourhood
  - Currently emits only to objects in the specified location
  - Should emit to everything in the neighbourhood (location + neighboring rooms)
  - Use exit transmit challenges to filter what propagates to neighboring rooms
  - Regular challenges parameter still filters individual recipients

- [ ] Add JS function to print to connection
  - Add `printToConnection(message)` JS function available on objects with active connections
  - Allows wizards to configure how sensory events are rendered
  - Example: In `user.js`: `addCallback('smell', ['emit'], (event) => printToConnection(event.description))`
  - Keeps sensory event definitions flexible - wizards decide what senses/skills exist
