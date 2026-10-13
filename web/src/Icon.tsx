const paths: Record<string, string> = {
  home: "M3 10.5 12 3l9 7.5M5 9v12h14V9M9 21v-8h6v8",
  conversations:
    "M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-1 1 1-5.5A8.5 8.5 0 1 1 21 11.5Z",
  commands: "m8 5-6 7 6 7m8-14 6 7-6 7m-3-16-2 18",
  activity: "M3 12h4l3-8 4 16 3-8h4",
  settings:
    "M12 3v3m0 12v3M3 12h3m12 0h3M5.6 5.6l2.1 2.1m8.6 8.6 2.1 2.1M5.6 18.4l2.1-2.1m8.6-8.6 2.1-2.1M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z",
  arrow: "M5 12h14m-6-6 6 6-6 6",
  shield: "m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Zm-4 9 3 3 5-6",
  phone:
    "M8 2h8a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2Zm3 16h2",
  spark: "m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5L12 3Z",
  search: "M10.5 18a7.5 7.5 0 1 1 0-15 7.5 7.5 0 0 1 0 15Zm5.5-2 5 5",
  moon: "M21 13a9 9 0 1 1-10-10 7 7 0 0 0 10 10Z",
  sun: "M12 3V1m0 22v-2M3 12H1m22 0h-2M4 4 2.5 2.5M21.5 21.5 20 20M4 20l-1.5 1.5M21.5 2.5 20 4M17 12a5 5 0 1 1-10 0 5 5 0 0 1 10 0Z",
  check: "m5 12 4 4L19 6",
  pause: "M8 4v16M16 4v16",
  play: "m7 3 14 9-14 9V3Z",
  plus: "M12 5v14M5 12h14",
  clock: "M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z",
  logout: "M9 3H4v18h5m4-14 5 5-5 5m-4-5h12",
  copy: "M8 8h12v12H8V8ZM4 16H2V2h14v2",
  warning: "m12 3 10 18H2L12 3Zm0 6v5m0 3v1",
};
export function Icon({ name, size = 20 }: { name: string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name] ?? paths.spark} />
    </svg>
  );
}
