import { Link } from "react-router";
import { Compass } from "lucide-react";
import { EmptyState } from "../components/ui/EmptyState";
import { Button } from "../components/ui/Button";

export default function NotFound() {
  return <EmptyState icon={<Compass />} title="Page not found" body="That page doesn't exist." action={<Link to="/"><Button>Back to Overview</Button></Link>} />;
}
