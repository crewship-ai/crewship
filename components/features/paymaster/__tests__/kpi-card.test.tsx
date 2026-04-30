import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { PaymasterKpiCard } from "@/components/features/paymaster/kpi-card"

describe("PaymasterKpiCard", () => {
  it("renders label + value + subtitle", () => {
    render(
      <PaymasterKpiCard label="Total spend" value="$42.50" subtitle="across 12 crews" />,
    )
    expect(screen.getByText("Total spend")).toBeInTheDocument()
    expect(screen.getByText("$42.50")).toBeInTheDocument()
    expect(screen.getByText("across 12 crews")).toBeInTheDocument()
  })

  it("numeric value renders without crashing", () => {
    render(<PaymasterKpiCard label="Calls" value={1234} />)
    expect(screen.getByText("1234")).toBeInTheDocument()
  })

  it("delta arrow ▲ shown for direction=up", () => {
    render(
      <PaymasterKpiCard
        label="Spend"
        value="$10"
        deltaLabel="+12%"
        deltaDirection="up"
      />,
    )
    expect(screen.getByText("▲")).toBeInTheDocument()
    expect(screen.getByText("+12%")).toBeInTheDocument()
  })

  it("delta arrow ▼ shown for direction=down", () => {
    render(
      <PaymasterKpiCard label="x" value="0" deltaLabel="-5%" deltaDirection="down" />,
    )
    expect(screen.getByText("▼")).toBeInTheDocument()
  })

  it("delta arrow hidden for direction=flat (no character rendered)", () => {
    render(
      <PaymasterKpiCard label="x" value="0" deltaLabel="0%" deltaDirection="flat" />,
    )
    expect(screen.queryByText("▲")).not.toBeInTheDocument()
    expect(screen.queryByText("▼")).not.toBeInTheDocument()
  })

  it("up=red when upIsGood=false (default — rising cost is bad)", () => {
    const { container } = render(
      <PaymasterKpiCard label="x" value="0" deltaLabel="+5%" deltaDirection="up" />,
    )
    expect(container.querySelector(".text-red-400")).toBeTruthy()
    expect(container.querySelector(".text-emerald-400")).toBeFalsy()
  })

  it("up=green when upIsGood=true (e.g. throughput, success rate)", () => {
    const { container } = render(
      <PaymasterKpiCard
        label="x"
        value="0"
        deltaLabel="+5%"
        deltaDirection="up"
        upIsGood
      />,
    )
    expect(container.querySelector(".text-emerald-400")).toBeTruthy()
  })

  it("down=green when upIsGood=false (cost dropping is good)", () => {
    const { container } = render(
      <PaymasterKpiCard label="x" value="0" deltaLabel="-5%" deltaDirection="down" />,
    )
    expect(container.querySelector(".text-emerald-400")).toBeTruthy()
  })

  it("subtitle is hidden when deltaLabel is provided (delta wins)", () => {
    render(
      <PaymasterKpiCard
        label="x"
        value="0"
        subtitle="should not appear"
        deltaLabel="+1%"
        deltaDirection="up"
      />,
    )
    expect(screen.queryByText("should not appear")).not.toBeInTheDocument()
    expect(screen.getByText("+1%")).toBeInTheDocument()
  })

  it("flat direction still renders deltaLabel without arrow", () => {
    render(
      <PaymasterKpiCard label="x" value="0" deltaLabel="±0%" deltaDirection="flat" />,
    )
    expect(screen.getByText("±0%")).toBeInTheDocument()
  })
})
