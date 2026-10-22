# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

- **Office operator (primary).** Works at a desk PC in the depot office. Watches the gate: reviews the queue of trucks the camera or a clerk captured, confirms or corrects container numbers, and looks at the live camera when a read is uncertain.
- **Depot owner / manager.** Checks how the gate is doing (today's trucks, what needed a person, accuracy), on a PC or a phone. The owner is also the buyer.
- **Gate clerk (backup role).** Uses a phone at the gate only when the fixed camera cannot read a container, or to add damage photos.
- **Prospective customers.** Other depot owners evaluating the product; worldwide market, Turkey first.

## Product Purpose

A gate system for container depots that reads incoming containers automatically (number, ISO type code, colour) from a fixed gate camera, with a person confirming every uncertain read, and keeps verified photos as training data for the owner's own offline reading model. It is the first module of complete depot management software sold to other depots, later joined by the owner's container sales website. Success is a fast gate with trustworthy records: no wrong container number goes into the depot's books.

## Positioning

The read is checked, not trusted. Every number is validated by the ISO 6346 check digit and cross-checked across several photos, and anything uncertain goes to a person with the photos and live video in front of them. The verified photos then train the depot's own model, so it gets better and less dependent on the internet over time.

## Operating Context

- A gate hub runs on a PC in the depot and keeps working when the internet is unreliable; screens are opened in a browser on the depot network.
- Fixed camera (first unit: Reolink RLC-810WA) with its own vehicle detection starts a photo burst per truck; live video is shown in the app.
- Each truck visit becomes a record in the queue: read automatically, then confirmed, corrected or refused by the operator.
- Phone capture remains as a fallback at the gate.
- Gate-in now; gate-out, damage photos, slips, pre-advice and depot management follow.

## Capabilities and Constraints

- Today: queue and truck detail with photos and number boxes, check-digit maths, camera setup and testing, live video, "read this frame" aiming aid, automatic photos on vehicle detection, phone capture, test read, training-data export, accuracy benchmark.
- Readings come from Gemini today; the owner's own model later.
- Languages: English by default, Turkish as an option. More languages may come with international sales.
- Must work on a modest depot PC and on phones over the local network (plain HTTP on the LAN).
- Terms: move / truck visit, gate-in, container number, ISO type code, check digit, camera visit, "no container".
- Undecided: the product brand name (to be proposed once the design direction is set).

## Brand Commitments

- A new, separate product brand (not the owner's container sales company brand). Name not yet chosen.
- First impression for a depot owner, in this order: impressive, trustworthy, then simple.

## Evidence on Hand

- One working depot (the owner's own) testing with yard stock; a partner depot with high gate volume willing to pilot.
- No customers, testimonials, accuracy figures or case studies yet. Do not fabricate any.

## Product Principles

1. A wrong container number is worse than a slow one: uncertainty is always shown and goes to a person.
2. The evidence travels with the decision: photos, boxes, check-digit maths and live video sit next to every number.
3. The gate never stops: the system degrades gracefully when the camera, the AI or the internet fails, and says so plainly.
4. Every confirmed read improves the system: verified photos become the depot's own training data.
5. Built to be sold: what one depot sees must convince the next one.
