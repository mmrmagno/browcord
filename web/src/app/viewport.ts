export interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

export function contentBox(
  boxWidth: number,
  boxHeight: number,
  mediaWidth: number,
  mediaHeight: number,
): Box {
  if (boxWidth <= 0 || boxHeight <= 0 || mediaWidth <= 0 || mediaHeight <= 0) {
    return { left: 0, top: 0, width: Math.max(1, boxWidth), height: Math.max(1, boxHeight) };
  }

  const mediaRatio = mediaWidth / mediaHeight;
  const boxRatio = boxWidth / boxHeight;

  if (boxRatio > mediaRatio) {
    const width = boxHeight * mediaRatio;
    return { left: (boxWidth - width) / 2, top: 0, width, height: boxHeight };
  }

  const height = boxWidth / mediaRatio;
  return { left: 0, top: (boxHeight - height) / 2, width: boxWidth, height };
}
