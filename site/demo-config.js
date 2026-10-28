// Public demo instance config for site/demo.html.
// baseUrl is the live read-only synthetic Aegis. Persona keys are filled in
// per launch (alpha keys are shared out of band and not committed).
window.AEGIS_DEMO = {
  baseUrl: "https://aegis.unified-designs.com",
  personas: {
    shire: {
      id: "shire",
      label: "Frodo’s circle",
      blurb: "Hobbits, Bilbo, and the first days on the road (April–July 2023).",
      key: "YOUR_SHIRE_KEY",
    },
    gondor: {
      id: "gondor",
      label: "South-kingdom",
      blurb: "Council, Hornburg, Pelennor, coronation — the wider war.",
      key: "YOUR_GONDOR_KEY",
    },
    lincoln: {
      id: "lincoln",
      label: "Abraham Lincoln",
      blurb: "Presidential desk, 1861–1865: inaugurations, Gettysburg, Appomattox.",
      key: "YOUR_LINCOLN_KEY",
    },
  },
};
