using Go = import "/go.capnp";
@0xd258d93c56221e58;
$Go.package("storage");
$Go.import("storage");

struct Object {
    id @0 :Data;
    # ID of this object.

    location @1 :Data;
    # ID of the Object containing this Object.

    content @2 :List(Data);
    # ID of all Objects contained by this Object.

    callbacks @3 :List(Text);
    # Names of all functions this Object exports.

    struct Skill {
        name @0 :Text;
        # Name of this skill.

        theoretical @1 :Float32;
        # Theoretical level of this skill.

        practical @2 :Float32;
        # Practical level of this skill.
    }

    skills @4 :List(Skill);
    # Skills of this Object.

    struct Challenge {
        skill @0 :Text;
        # Name of the skill this challenges.

        legel @1 :Float32;
        # Level of challenge.
    }

    struct Description {
        short @0 :Text;
        # Short description text (when not being actively looked at).

        long @1 :Text;
        # Long description text (when being actively looked at).

        tags @2 :List(Text);
        # Object tags (user, mob, monster, aggro, whatever) for code detection provided by this description.

        challenges @3 :List(Challenge);
        # Skill challenges to overcome to detect this description.
    }

    descriptions @5 :List(Description);
    # Descriptions of this Object. Ordered by decreasing difficulty, the first one detected is the one shown.

    state @6 :Text;
    # The global variables of the Object as JSON.

    source @7 :Text;
    # Path to the JavaScript source controlling this Object.
}