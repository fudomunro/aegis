// Public demo instance config for site/demo.html.
// Replace baseUrl and keys when the demo host is live. These credentials are
// intentionally public: they only reach a read-only synthetic instance.
window.AEGIS_DEMO = {
  baseUrl: "https://YOUR_DEMO_HOST",
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
