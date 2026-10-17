# Message action visual prototypes

Generated with the built-in image generation tool on 2026-10-17. Synthetic
conversation content only. These are raster design proposals, not working UI.

- `components-v2.png`: selected component direction after two generation rounds.
  Shows the desktop RCS action menu, reaction chip, quote rendering, reply composer,
  proposed edit composer, and a conservative SMS action menu.
- `desktop-mobile-v1.png`: first pass retained as the mobile bottom-sheet reference.
  The second pass replaces its right-hand panels to show edit and SMS states.

The existing `docs/images/conversations.png` and CSS tokens informed the design.
Keep the current app's lavender surfaces, teal accent, compact typography, borders,
and bubble styling. Match actual CSS rather than reproducing incidental generated
shadows, gradients, or small spacing differences.

The edit composer is conditional on proving outbound transport support. The SMS
menu illustrates current conservative eligibility, not a claim that SMS reactions
can never be supported. The backend capability response controls the shared menu.
An eligible outgoing-message menu can include Edit when that capability is verified;
the illustrated incoming-message menu correctly does not offer it.

Review outcome: the first pass established the desktop and touch interactions; the
second added the missing edit composer and SMS distinction while preserving the
main screen. Both images were visually inspected. No app builds, automated tests,
or live transport operations were performed for these design artifacts.

The exact final refinement prompt is saved in `final-prompt.txt`.
