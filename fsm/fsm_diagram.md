# Volume FSM — Lifecycle

Initial state: `pending`

## States

- `pending` — volume created, waiting for provisioning
- `creating` — provisioning in progress on the backend
- `available` — ready to be attached or deleted
- `attaching` — attachment in progress to a node
- `attached` — attached to a compute node
- `detaching` — detachment in progress
- `deleting` — deletion in progress
- `deleted` — permanently deleted
- `error` — unrecoverable error

## Transitions

| Event | From | To |
|-------|------|----|
| `create` | `pending` | `creating` |
| `ready` | `creating` | `available` |
| `error` | `creating \| attaching \| detaching \| deleting` | `error` |
| `attach` | `available` | `attaching` |
| `attached` | `attaching` | `attached` |
| `detach` | `attached` | `detaching` |
| `detached` | `detaching` | `available` |
| `delete` | `available` | `deleting` |
| `deleted` | `deleting` | `deleted` |

## Mermaid diagram

```mermaid
stateDiagram-v2
    [*] --> pending
    attached --> detaching: detach
    attaching --> attached: attached
    attaching --> error: error
    available --> attaching: attach
    available --> deleting: delete
    creating --> error: error
    creating --> available: ready
    deleting --> deleted: deleted
    deleting --> error: error
    detaching --> available: detached
    detaching --> error: error
    pending --> creating: create
```

## Graphviz diagram (DOT)

```dot
digraph fsm {
    "attached" -> "detaching" [ label = "detach" ];
    "attaching" -> "attached" [ label = "attached" ];
    "attaching" -> "error" [ label = "error" ];
    "available" -> "attaching" [ label = "attach" ];
    "available" -> "deleting" [ label = "delete" ];
    "creating" -> "error" [ label = "error" ];
    "creating" -> "available" [ label = "ready" ];
    "deleting" -> "deleted" [ label = "deleted" ];
    "deleting" -> "error" [ label = "error" ];
    "detaching" -> "available" [ label = "detached" ];
    "detaching" -> "error" [ label = "error" ];
    "pending" -> "creating" [ label = "create" ];

    "attached";
    "attaching";
    "available";
    "creating";
    "deleted";
    "deleting";
    "detaching";
    "error";
    "pending" [color = "red"];
}
```
