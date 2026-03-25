# Board Plan Guide

Board plan is a cycling variant of matrix. Small fixed-size matrices fill with distributors. When a board fills completely, the top position cycles out and earns a fixed commission. The board splits into new boards.

## How It Works

1. A small matrix (e.g., 2x2) is created as a "board"
2. Members fill the board from top to bottom in BFS order
3. When all positions are filled, the person at the top cycles out
4. The board splits — each direct child of the root becomes the root of a new board
5. The cycled-out person re-enters at the bottom of another board (if re-entry is enabled)
6. The process repeats

## Configuration

```yaml
structures:
  - name: Sales Board
    type: board_plan
    structure:
      width: 2
      height: 2
    board_cycling:
      cycle_commission: 500.00
      re_entry_enabled: true
      re_entry_position: bottom
      max_cycles_per_period: 5
      max_cascade_depth: 10
      stall_threshold_periods: 3
      inactive_compression: true
  - name: Sponsor Tree
    type: unilevel
    # ... unilevel config required
```

### Dimension Caps

Board plans use small matrices. Width is capped at 2-5 and height at 1-4.

| Config | Positions | Notes |
|--------|-----------|-------|
| 2x2    | 7         | Most common |
| 2x3    | 15        | Medium |
| 3x2    | 13        | Wide |
| 3x3    | 40        | Large |
| 5x4    | 781       | Maximum allowed |

### Companion Unilevel

Board plan structures require a companion unilevel structure. The board plan handles cycling and cycle commissions only. The unilevel handles sponsor-based level commissions.

### Board Cycling Options

| Field | Type | Description |
|-------|------|-------------|
| `cycle_commission` | number > 0 | Fixed dollar amount per cycle |
| `re_entry_enabled` | boolean | Auto re-enter after cycling out |
| `re_entry_position` | `bottom` or `sponsor_board` | Where re-entered members are placed |
| `max_cycles_per_period` | integer >= 1 | Cap on cycle earnings per period |
| `max_cascade_depth` | integer >= 1 | Max chained cycles per operation (default 10) |
| `stall_threshold_periods` | integer >= 1 | Inactive periods before a board is stalled |
| `inactive_compression` | boolean | Remove inactive members from boards |

## Re-Entry Modes

**Bottom:** Cycled member is placed in the oldest board with an open slot. Fills older boards first.

**Sponsor Board:** Cycled member is placed in their sponsor's board. Falls back to Bottom if the sponsor's board is full.

## Stall Detection

Go drives stall detection by passing a cutoff timestamp. The engine returns boards that haven't had activity since that time. Go converts the configured `stall_threshold_periods` into a timestamp.

Stalled boards can be dissolved. All members go to a displaced pool and are placed before new enrollees.

## Inactive Compression

When enabled, Go can pass a list of inactive member IDs to the engine. Each inactive member is removed and the board compacts to fill the gap. This prevents boards from stalling due to individual member inactivity.
