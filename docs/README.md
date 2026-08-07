# Switchyard Documentation

This directory contains reference documentation for every Switchyard feature and concept.

| Document | Description |
|----------|-------------|
| [concepts.md](concepts.md) | Glossary of terms used throughout the project and config |
| [architecture.md](architecture.md) | The three-stage request pipeline and core design constraints |
| [config-reference.md](config-reference.md) | Every configuration field, its type, default, and validation rules |
| [backends.md](backends.md) | Backend configuration, round-robin selection, and error handling |
| [routing.md](routing.md) | Location-based routing — prefix/regex matching, proxy and static types |
| [variables.md](variables.md) | All available `$variable` placeholders and where they can be used |
| [set-headers.md](set-headers.md) | Injecting request headers with variable substitution |
| [logging.md](logging.md) | Custom log format, available fields, outputs, and body capture |
| [extending.md](extending.md) | Using Switchyard as an SDK — overriding pipeline stages with your own Go code |
| [testing.md](testing.md) | How the test suite is organized, how to test each stage, and the contributor rule |

Start with [concepts.md](concepts.md) if you are new to the project, then [architecture.md](architecture.md) to understand the request flow before diving into individual feature docs. For customizing behavior with your own code, see [extending.md](extending.md).
