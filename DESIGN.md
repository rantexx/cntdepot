---
name: Depot Gate
description: Gate reading for container depots, set as the terminal's own safety signage.
colors:
  porcelain-ground: "#f4f4f1"
  panel-white: "#ffffff"
  graphite-ink: "#1e2226"
  slate-muted: "#5b6267"
  warm-rule: "#cbcbc3"
  warm-rule-soft: "#e2e2dc"
  bar-track: "#dad9d2"
  asphalt-rail: "#1e2226"
  rail-ink: "#dcddd8"
  rail-muted: "#a9ada9"
  rail-on: "#2b3137"
  rail-rule: "#353b41"
  safe-green: "#0d8045"
  signal-yellow: "#f2c200"
  safety-blue: "#1f5fa8"
  prohibition-red: "#c8202a"
  neutral-grey: "#6c7378"
  read-orange: "#ff9a1f"
  camera-black: "#101316"
  night-ground: "#14171a"
  night-panel: "#1c2024"
  night-ink: "#eceee9"
  night-muted: "#a4a9a7"
  night-rule: "#353b41"
  night-rule-soft: "#2a2f34"
  night-rail: "#0d0f11"
  night-safety-blue: "#2c6dbb"
  night-neutral-grey: "#6c7378"
  night-focus: "#5b95dd"
typography:
  display:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "clamp(30px, 3.1vw, 46px)"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "0.03em"
    fontFeature: "tnum"
  figure:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "44px"
    fontWeight: 700
    lineHeight: 1
    fontFeature: "tnum"
  headline:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "30px"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "0.03em"
  title:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "22px"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "0.04em"
  row:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "20px"
    fontWeight: 500
    lineHeight: 1.35
    fontFeature: "tnum"
  body:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "17px"
    fontWeight: 500
    lineHeight: 1.35
    fontFeature: "tnum"
  label:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 700
    lineHeight: 1.3
    letterSpacing: "0.06em"
  column-head:
    fontFamily: "Barlow Semi Condensed, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 600
    letterSpacing: "0.08em"
rounded:
  tile: "2px"
  sign: "3px"
  field: "4px"
  button: "6px"
  plate: "14px"
spacing:
  xs: "6px"
  sm: "10px"
  row: "14px"
  md: "16px"
  lg: "24px"
components:
  button-primary:
    backgroundColor: "{colors.safety-blue}"
    textColor: "{colors.panel-white}"
    typography: "{typography.body}"
    rounded: "{rounded.button}"
    padding: "0 18px"
    height: "44px"
  button-secondary:
    backgroundColor: "{colors.panel-white}"
    textColor: "{colors.graphite-ink}"
    typography: "{typography.body}"
    rounded: "{rounded.button}"
    padding: "0 18px"
    height: "44px"
  button-secondary-hover:
    backgroundColor: "{colors.warm-rule-soft}"
  button-danger:
    backgroundColor: "{colors.panel-white}"
    textColor: "{colors.prohibition-red}"
    rounded: "{rounded.button}"
    height: "44px"
  input:
    backgroundColor: "{colors.panel-white}"
    textColor: "{colors.graphite-ink}"
    typography: "{typography.body}"
    rounded: "{rounded.field}"
    padding: "8px 12px"
    height: "44px"
  sign-safe:
    backgroundColor: "{colors.safe-green}"
    textColor: "{colors.panel-white}"
    typography: "{typography.label}"
    rounded: "{rounded.sign}"
    padding: "3px 9px"
  sign-caution:
    backgroundColor: "{colors.signal-yellow}"
    textColor: "{colors.graphite-ink}"
    typography: "{typography.label}"
    rounded: "{rounded.sign}"
    padding: "3px 9px"
  sign-act:
    backgroundColor: "{colors.safety-blue}"
    textColor: "{colors.panel-white}"
    typography: "{typography.label}"
    rounded: "{rounded.sign}"
    padding: "3px 9px"
  sign-stop:
    backgroundColor: "{colors.prohibition-red}"
    textColor: "{colors.panel-white}"
    typography: "{typography.label}"
    rounded: "{rounded.sign}"
    padding: "3px 9px"
  sign-none:
    backgroundColor: "{colors.neutral-grey}"
    textColor: "{colors.panel-white}"
    typography: "{typography.label}"
    rounded: "{rounded.sign}"
    padding: "3px 9px"
  sign-reading:
    backgroundColor: "{colors.panel-white}"
    textColor: "{colors.graphite-ink}"
    typography: "{typography.label}"
    rounded: "{rounded.sign}"
    padding: "3px 9px"
  sign-plate:
    backgroundColor: "{colors.safety-blue}"
    textColor: "{colors.panel-white}"
    typography: "{typography.display}"
    rounded: "{rounded.plate}"
    padding: "14px 20px"
  chip:
    backgroundColor: "{colors.warm-rule-soft}"
    textColor: "{colors.graphite-ink}"
    rounded: "{rounded.sign}"
    padding: "2px 8px"
  card:
    backgroundColor: "{colors.panel-white}"
    rounded: "{rounded.field}"
    padding: "18px"
  rail:
    backgroundColor: "{colors.asphalt-rail}"
    textColor: "{colors.rail-ink}"
    width: "216px"
  rail-link-active:
    backgroundColor: "{colors.rail-on}"
    textColor: "{colors.panel-white}"
    height: "46px"
  visit-row:
    textColor: "{colors.graphite-ink}"
    typography: "{typography.row}"
    height: "48px"
---

# Design System: Depot Gate

## Overview

**Creative North Star: "Terminal Safety Signage"**

The app reads like the terminal's own signs. A graphite asphalt rail and a porcelain work ground carry everything, divided by 1px warm-grey rules that form the grid. Colour appears only when it means something: every truck's state wears the ISO 3864 safety colour the yard already obeys. Green means confirmed, signal yellow means a person is needed, safety blue means act now, red means refused, grey means no container, and an outlined sign means the system is still reading.

One condensed sign face, Barlow Semi Condensed with tabular numerals, sets both display and data. Container numbers are set the way they are painted on the doors: owner code, serial, and the check digit in its own box. Enamel sign plates with a double inner keyline and rounded corners carry the lane and the current number. The surface is flat and dense, with fixed row furniture: each column of a list sits in the same place on every row, and each truck's gate time is drawn against the target.

Day and Night are the same signs on two grounds. Night re-keys the neutrals and slightly lifts safety blue and grey; the state colours keep their meaning.

**Key Characteristics:**
- Safety colours are reserved for state; the chrome is graphite, porcelain and warm-grey rules.
- One family (Barlow Semi Condensed 500/600/700), tabular numerals everywhere.
- Boxed check digit on every 11-character container number.
- Enamel plates with a double inset keyline; flat everywhere else.
- Fixed-column rows with a state tile on the left and a gate-time bar on the right.
- English labels are uppercase and letter-spaced; Turkish labels stay in normal case.

## Colors

A graphite-and-porcelain neutral ground with the ISO safety colours used only to say what state a truck is in.

### Primary
- **Safety Blue** (safety-blue): the "act now" colour. Primary buttons (Confirm, Save, Start), the sign plate while a person must act on the current read, links, the text caret, checkbox accents, and the focus ring in Day. In Night it lifts to night-safety-blue.

### Secondary
- **Safe Green** (safe-green): confirmed / accepted. State signs and tiles, camera and engine "on" lights, check-digit-valid marks, the gate-time bar fill while under target.
- **Signal Yellow** (signal-yellow): needs a person. Review signs and tiles, the review count in the rail, warning squares, and text selection. Text on yellow is always graphite ink, never white.
- **Prohibition Red** (prohibition-red): refused, failed, or over target. Refused signs, error text, danger buttons, invalid check digits, the gate-time bar once it passes the target.

### Tertiary
- **Read Orange** (read-orange): machine annotation only. The engine's number box over a photo and the keyline around the read crop. It never marks a truck's state.

### Neutral
- **Porcelain Ground** (porcelain-ground): the work area background; also the rail's active-link marker and the brand mark.
- **Panel White** (panel-white): cards, the read panel, fields, secondary buttons, outlined "reading" signs; text on safety-colour fills.
- **Graphite Ink** (graphite-ink): body text, button borders, the outline of the "reading" sign, the target tick on gate-time bars, the rule above a list's column heads.
- **Slate Muted** (slate-muted): labels, column heads, secondary text, dates beside titles.
- **Warm Rule** (warm-rule) and **Warm Rule Soft** (warm-rule-soft): 1px grid rules and field borders; row hover and chip fill.
- **Bar Track** (bar-track): the unfilled part of a gate-time bar.
- **Asphalt Rail** (asphalt-rail) with **Rail Ink**, **Rail Muted**, **Rail On** and **Rail Rule**: the navigation rail, its text, its hover/active fill and its dividers.
- **Neutral Grey** (neutral-grey): no container, unknown or off states.
- **Camera Black** (camera-black): behind live video and crops; overlays on video use it at 86–88% opacity.
- **Night set** (night-ground, night-panel, night-ink, night-muted, night-rule, night-rule-soft, night-rail, night-safety-blue, night-neutral-grey, night-focus): the same roles on a dark ground. Safe green, signal yellow and prohibition red do not change.

### Named Rules
**The State-Only Rule.** Safety colours mark state and nothing else. No safety colour is used for decoration, branding, section accents or charts that are not about a truck's state.

**The Yellow-Means-You Rule.** Signal yellow appears only where a person is needed (review state, review count, warnings). Text on it is graphite ink.

**The Two-Grounds Rule.** Night follows `prefers-color-scheme` unless the person chose Day or Night in the rail, which is stored per browser as `data-theme`. Every new colour must be a custom property with a Night value, or a state colour that holds in both.

## Typography

**Display Font:** Barlow Semi Condensed (with system-ui, sans-serif), self-hosted at weights 500, 600, 700
**Body Font:** the same family
**Label/Mono Font:** the same family; "mono" text is the same face at 600 with 0.03em tracking, not a monospace

**Character:** One condensed, sign-like grotesque carries everything from the 46px plate number to 13px column heads. Tabular numerals are on at the root, so times, counts and container numbers line up.

### Hierarchy
- **Display** (700, clamp(30px, 3.1vw, 46px), 1.1, 0.03em): the container number on the sign plate.
- **Figure** (700, 44px, 1; 34px on narrow screens): the day's counts in the today strip. Big figures elsewhere (manual entry 30px, capture result 40px) use the same weight.
- **Headline** (700, 30px, 1.1, 0.03em, uppercase): the page title in the page head, with an 18px muted 500 date or sub-line beside it in normal case.
- **Title** (700, 22px, 0.04em, uppercase): section headings; 19px 700 sentence case for third-level headings.
- **Row** (500, 20px; 18px narrow): visit rows. Container numbers in rows are 600 with 0.03em tracking (22px in the review queue).
- **Body** (500, 17px, 1.35): text, buttons, fields.
- **Label** (700, 14px, 0.06em, uppercase): signs and chips (chips 13px). Field labels are 600 15px 0.05em uppercase muted; panel heads and fact labels are 600 13–15px 0.07em uppercase muted; column heads 600 13px 0.08em.

### Named Rules
**The One Face Rule.** Barlow Semi Condensed is the only family. Hierarchy comes from size, weight (500/600/700), case and tracking, never from a second face.

**The Dotted-İ Rule.** Uppercase with letter-spacing is for English labels. Under `lang="tr"` all text-transform is switched off, so Turkish labels keep normal case and never gain a dot on every capital İ. Never hard-code capitals in the strings themselves.

**The Painted Number Rule.** An 11-character container number is set as owner code, a space, the six-digit serial, then the check digit in its own 2px box (3px on plates), as painted on the doors.

## Layout

A 216px graphite rail on the left, sticky at full height, and a work area with 24px gutters and a 1480px maximum width. Each page opens with a page head at least 60px tall that bleeds to the gutters, with a 1px rule under it; it holds the title on the left and, where relevant, a state key on the right.

Lists use fixed row furniture. A visit row is a grid of a 12px state tile, 64px time, 200px container number, 64px type code, a flexible state column (sign plus reason), and a 280px gate-time column, with a 14px gap, at least 48px tall, divided by 1px rules. Column heads sit above under a 1px ink rule. The review queue follows the same pattern (12px tile, 96px photo thumbnail, flexible content).

The live gate stacks a today strip (four equal counts plus a 1.6-fraction gate-time cell, divided by vertical rules) above a live row: the camera on the left and a 420px read panel on the right. Fact rows inside the panel use four equal columns divided by vertical rules.

Spacing is tight and repeated: 6px inside groups, 10px between controls, 14px between row columns, 16px between blocks, 24px page gutters.

At 900px and below the rail becomes a top bar (wordmark, switches, then a full-width wrapping nav row with a 3px bottom marker for the active page), gutters drop to 16px, the today strip goes to two columns with the gate-time cell spanning the full width, the live row stacks, visit rows collapse to tile, time and a two-line body with the type column and column heads hidden, and the state key is hidden.

## Elevation & Depth

Flat. There are no drop shadows, no gradients and no glows. Depth comes from the dark rail against the light ground, from 1px rules, and from inset keylines. Every `box-shadow` in the system is inset and used as a line, never as a lift.

### Shadow Vocabulary
- **Plate double keyline** (`box-shadow: inset 0 0 0 4px <fill>, inset 0 0 0 7px <line>`): the enamel sign plate's inner border, white on blue/green/red/grey, graphite on yellow.
- **Lane plate keyline** (`border: 3px solid #1e2226; box-shadow: inset 0 0 0 2px #fff, inset 0 0 0 4px #1e2226`): the white lane plate over the camera.
- **Outline sign** (`box-shadow: inset 0 0 0 2px var(--ink)`): the "reading" state sign and tile.
- **Active rail marker** (`box-shadow: inset 4px 0 0 #f4f4f1`; `inset 0 -3px 0` on narrow screens): the current page in the nav.
- **Read keyline** (`box-shadow: inset 0 0 0 2px #ff9a1f`): around the read crop.

### Named Rules
**The Keyline Not Lift Rule.** A shadow is only ever an inset line. Nothing floats above the page.

## Shapes

Small, firm corners: 2px for tiles, check-digit boxes and status squares, 3px for signs, chips and badges, 4px for cards, fields, tabs and video, 6px for buttons and the lane plate. The one large radius is the 14px enamel sign plate. Status lights, legend markers and step bullets are 10–12px squares with 2px corners, not dots. Segmented controls (language, theme, tabs) are one bordered box with flat internal dividers and square inner corners.

## Components

### Buttons
Heavy, bordered and plain.
- **Shape:** gently squared (6px), at least 44px tall, 18px side padding, 17px 700.
- **Primary:** safety blue fill and border, white text, uppercase with 0.08em tracking. Hover brightens it slightly (`filter: brightness(1.08)`).
- **Secondary:** panel fill, 2px graphite border, graphite text, sentence case; hover fills with warm-rule-soft.
- **Danger:** secondary with prohibition-red text and border.
- **Big:** full width, 60px tall, 21px. The read panel's two actions are 52px, 19px.
- **Disabled:** 45% opacity.
- **Focus:** 3px focus-colour outline, 2px offset, on every focusable element.

### Signs (state chips)
The state vocabulary. Uppercase 14px 700 label on a solid safety-colour fill with a 3px corner: safe green, signal yellow (graphite text), safety blue, prohibition red, neutral grey. "Reading" and "capturing" are an outlined sign: panel fill, ink text, 2px inset ink line. The same mapping drives the 12×26px state tile at the left of each row.

### Chips (tags)
Neutral metadata tags (camera, test, read): warm-rule-soft fill, ink text, 13px 700 uppercase, 3px corner. They never use safety colours.

### Cards / Containers
- **Corner Style:** 4px.
- **Background:** panel white.
- **Shadow Strategy:** none; see Elevation.
- **Border:** 1px warm rule.
- **Internal Padding:** 18px cards, 16px read panel.

### Inputs / Fields
- **Style:** panel fill, 1px warm-rule border, 4px corner, 44px tall, 8px 12px padding, 17px 500. Labels sit above in 15px 600 uppercase muted.
- **Focus:** border turns safety blue, plus the global 3px focus outline. The caret is safety blue.
- **Manual number entry:** 30px 700 with 0.04em tracking.

### Navigation
A graphite rail: a 60px wordmark row (22px 700 uppercase white, with a square brand mark of porcelain and graphite rings), then 46px links at 19px in rail-muted. Hover and active fill with rail-on and turn white; active is 700 with a 4px porcelain bar on the left. A signal-yellow review count sits at the right of its link. The foot holds the language and Day/Night segmented switches (the pressed half is porcelain with graphite text) and camera/engine status squares.

### Sign Plate (signature)
The current truck's number on an enamel plate: 14px corner, 14px 20px padding, centred, a double inset keyline, an uppercase 14px 600 label (0.1em) over the display-size number with its check digit boxed in 3px. The fill follows the truck's state; while a person must act it is safety blue and the label asks them to confirm the number.

### Lane Plate and Camera Overlays (signature)
On the camera: a white lane plate top-left (32px 700, triple graphite/white keyline, 6px corner), a LIVE badge top-right, and progress labels along the bottom, both on camera-black at 86–88% opacity with 3px corners. The engine's number box is drawn in read orange.

### Visit Row and Gate Time (signature)
One truck per row with fixed furniture (see Layout); the whole row is a link with a warm-rule-soft hover. Gate time is a 150×8px bar on the track colour, filled green up to the 60 s target and red once over, with a 2px ink tick at the midpoint marking the target, and the time beside it in 600.

### Read Crop
The part of the photo where the number was read, cut with a margin from the engine's box, on camera black with a 4px corner and a 2px read-orange inset line, captioned in 14px muted.

## Do's and Don'ts

### Do:
- **Do** give every truck state its ISO colour through the sign, tile and plate mapping: green confirmed, yellow needs a person, blue act now, red refused, grey no container, outlined for reading.
- **Do** set every 11-character container number with its check digit boxed.
- **Do** keep rows in fixed columns and draw gate time against the 60 s target tick.
- **Do** declare new colours as custom properties with a Night value.
- **Do** use uppercase with tracking for English labels and leave the strings in normal case, so `lang="tr"` can switch it off.
- **Do** keep controls at least 44px tall and keep the 3px focus outline.

### Don't:
- **Don't** use a safety colour for decoration, branding or emphasis that is not a state.
- **Don't** put white text on signal yellow.
- **Don't** add drop shadows, gradients or glows; depth is rules and inset keylines.
- **Don't** add a second typeface or a monospace for numbers; tabular Barlow Semi Condensed already aligns them.
- **Don't** use read orange for anything but the machine's read box and crop.
- **Don't** use round dots for status lights; status markers are small squares with 2px corners.
