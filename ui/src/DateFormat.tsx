import { createContext, useContext, type ReactNode } from "react";
import { DEFAULT_DATE_FORMAT, normalizeDateFormat, type DateFormat } from "./date";
const DateFormatContext = createContext<DateFormat>(DEFAULT_DATE_FORMAT);
export function DateFormatProvider({
  value,
  children,
}: {
  value?: DateFormat;
  children: ReactNode;
}) {
  return (
    <DateFormatContext.Provider value={normalizeDateFormat(value)}>
      {children}
    </DateFormatContext.Provider>
  );
}
export const useDateFormat = () => useContext(DateFormatContext);
