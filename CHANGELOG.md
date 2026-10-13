# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-10-13

### Added

- Payment method listing through `Method` and `Methods`. `Methods` reads
  `GET /v2.0/method.json` and returns the enabled merchant payment methods
  with their provider identifier, group, display names and logo URLs. The
  `comgatest` simulator reports a deterministic method list.

## [0.2.0] - 2026-10-21

### Added

- Pre-authorization support through `CreateInput.Preauth`, `CaptureInput`,
  `Capture` and `CancelPreauth`. `Capture` confirms a pre-authorized card
  payment in full or in part; `CancelPreauth` releases the blocked amount.
- Recurring payment support through `CreateInput.InitRecurring`,
  `RecurringInput`, `RecurringCharge` and `Recurring`. `Recurring` charges a
  stored payment method in the background and returns the new transaction ID.
- Background notification parsing through `Notification` and
  `ParseNotification`. The parser validates the form-encoded callback fields
  and documents that the fields are not signed, so callers must verify the
  payment with `Status` before acting on a notification.
- Tests that pin the partial refund request fields and the rejected refund
  error mapping. `Refund` already accepted partial amounts; its behavior is
  unchanged.

### Changed

- `Refund` and the new `Capture` share one mutation response classifier.
  Behavior is unchanged.
- `Cancel` and the new `CancelPreauth` share one cancellation response
  classifier. Behavior is unchanged.
