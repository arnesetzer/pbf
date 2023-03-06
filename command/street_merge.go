package command

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/missinglink/pbf/sqlite"

	"github.com/missinglink/pbf/handler"
	"github.com/missinglink/pbf/lib"
	"github.com/missinglink/pbf/parser"
	"github.com/missinglink/pbf/proxy"
	"github.com/missinglink/pbf/tags"

	geo "github.com/paulmach/go.geo"
	"github.com/urfave/cli"
)

type street struct {
	Path *geo.Path
	Name string
}

type config struct {
	Format          string
	Delim           string
	ExtendedColumns bool
	Path            bool
}

func (s *street) Print(conf *config) {

	// geojson
	// feature := s.Path.ToGeoJSON()
	// for _, way := range s.Ways {
	// 	for k, v := range way.Tags {
	// 		feature.SetProperty(k, v)
	// 	}
	// 	feature.SetProperty("id", way.ID)
	// }
	//
	// json, _ := feature.MarshalJSON()
	// fmt.Println(string(json))

	var cols []string

	switch conf.Format {
	case "geojson":
		bytes, err := s.Path.ToGeoJSON().MarshalJSON()
		if nil != err {
			log.Println("failed to marshal geojson")
			os.Exit(1)
		}
		cols = append(cols, string(bytes))
	case "wkt":
		cols = append(cols, s.Path.ToWKT())
	default:
		cols = append(cols, s.Path.Encode(1.0e6))
	}

	// extended columns
	if true == conf.ExtendedColumns {
		// mid-point centroid
		var centroid = s.Path.Interpolate(0.5)
		cols = append(cols, strconv.FormatFloat(centroid.Lng(), 'f', 7, 64))
		cols = append(cols, strconv.FormatFloat(centroid.Lat(), 'f', 7, 64))

		// geodesic distance in meters
		cols = append(cols, strconv.FormatFloat(s.Path.GeoDistance(), 'f', 0, 64))

		// bounds
		var bounds = s.Path.Bound()
		var sw = bounds.SouthWest()
		var ne = bounds.NorthEast()
		cols = append(cols, strconv.FormatFloat(sw.Lng(), 'f', 7, 64))
		cols = append(cols, strconv.FormatFloat(sw.Lat(), 'f', 7, 64))
		cols = append(cols, strconv.FormatFloat(ne.Lng(), 'f', 7, 64))
		cols = append(cols, strconv.FormatFloat(ne.Lat(), 'f', 7, 64))
	}

	cols = append(cols, s.Name)
	fmt.Println(strings.Join(cols, conf.Delim))
}

// StreetMerge cli command
func StreetMerge(c *cli.Context) error {
	// config
	var conf = &config{
		Format:          "polyline",
		Delim:           "\x00",
		ExtendedColumns: c.Bool("extended"),
		Path:            c.Bool("path"),
	}
	switch strings.ToLower(c.String("format")) {
	case "geojson":
		conf.Format = "geojson"
	case "wkt":
		conf.Format = "wkt"
	}
	if "" != c.String("delim") {
		conf.Delim = c.String("delim")
	}

	// open sqlite database connection
	// note: sqlite is used to store nodes and ways
	filename := lib.TempFileName("pbf_", ".temp.db")
	defer os.Remove(filename)
	conn := &sqlite.Connection{}
	conn.Open(filename)
	defer conn.Close()

	// parse
	parsePBF(c, conn, conf.Path)
	var streets = generateStreetsFromWays(conn)
	var joined = joinStreets(streets)

	// print streets
	for _, street := range joined {
		street.Print(conf)
	}

	// fmt.Println(len(ways))
	// fmt.Println(len(nodes))

	return nil
}

func joinStreets(streets []*street) []*street {
	var reversePath = func(path *geo.Path) {
		for i := path.PointSet.Length()/2 - 1; i >= 0; i-- {
			opp := path.PointSet.Length() - 1 - i
			path.PointSet[i], path.PointSet[opp] = path.PointSet[opp], path.PointSet[i]
		}
	}

	// points do not have to be exact matches
	//var distanceTolerance = 3.65 * 6 // width of 6 lanes (in meters)

	for i := 0; i < len(streets); i++ {
		for j := 0; j < len(streets); j++ {
			//continue if street is the same
			if i == j {
				continue
			}
			var intersects = streets[i].Path.Intersects(streets[j].Path)

			//Check if same name and streets intersect with each other
			if streets[i].Name == streets[j].Name && intersects {
				distanceList := make(map[string]float64)
				//ff = firstFirst, fl = firstLast, etc.
				distanceList["ff"] = streets[i].Path.First().DistanceFrom(streets[j].Path.First())
				distanceList["fl"] = streets[i].Path.First().DistanceFrom(streets[j].Path.Last())
				distanceList["lf"] = streets[i].Path.Last().DistanceFrom(streets[j].Path.First())
				distanceList["ll"] = streets[i].Path.Last().DistanceFrom(streets[j].Path.Last())

				//Sort by Value asc
				keys := make([]string, 0, len(distanceList))
				for k := range distanceList {
					keys = append(keys, k)
				}

				sort.SliceStable(keys, func(i, j int) bool {
					return distanceList[keys[i]] < distanceList[keys[j]]
				})
				//Sorting done

				//Get first and smallest distance
				var smallestDistance float64
				var DistanceType string
				for k, v := range distanceList {
					smallestDistance = v
					DistanceType = k
					break
				}

				if smallestDistance > 0.0 {
					log.Println("Street already merged, result may be a little bit weired on street", streets[i].Name)
				}
				switch DistanceType {
				case "ff":

					reversePath(streets[i].Path)

					for k := 0; k < len(streets[j].Path.Points()); k++ {
						streets[i].Path.Push((*geo.Point)(&streets[j].Path.Points()[k]))
					}
					reversePath(streets[i].Path)

				case "fl":

					reversePath(streets[i].Path)
					reversePath(streets[j].Path)

					for k := 0; k < len(streets[j].Path.Points()); k++ {
						streets[i].Path.Push((*geo.Point)(&streets[j].Path.Points()[k]))
					}
					reversePath(streets[i].Path)
				case "lf":

					for k := 0; k < len(streets[j].Path.Points()); k++ {
						streets[i].Path.Push((*geo.Point)(&streets[j].Path.Points()[k]))
					}
				case "ll":

					reversePath(streets[j].Path)

					for k := 0; k < len(streets[j].Path.Points()); k++ {
						streets[i].Path.Push((*geo.Point)(&streets[j].Path.Points()[k]))
					}
				}
				//Removed merged street
				streets = append(streets[:j], streets[j+1:]...)

			}

		}
	}

	return streets
}

func loadStreetsFromDatabase(conn *sqlite.Connection, callback func(*sql.Rows)) {
	rows, err := conn.GetDB().Query(`
	SELECT
		ways.id,
		(
			SELECT GROUP_CONCAT(( nodes.lon || '#' || nodes.lat ))
			FROM way_nodes
			JOIN nodes ON way_nodes.node = nodes.id
			WHERE way = ways.id
			ORDER BY way_nodes.num ASC
		) AS nodeids,
		(
			SELECT value
			FROM way_tags
			WHERE ref = ways.id
			AND key = 'name'
			LIMIT 1
		) AS name
	FROM ways
	ORDER BY ways.id ASC;
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		callback(rows)
	}
	err = rows.Err()
	if err != nil {
		log.Fatal(err)
	}
}

func generateStreetsFromWays(conn *sqlite.Connection) []*street {
	var streets []*street

	loadStreetsFromDatabase(conn, func(rows *sql.Rows) {

		var wayid int
		var nodeids, name string
		var maybeNodeIds sql.NullString
		err := rows.Scan(&wayid, &maybeNodeIds, &name)
		if err != nil {
			log.Fatal(err)
		}

		// handle the case where nodeids is NULL
		// note: this can occur when another tool has stripped the
		// nodes but left the ways which reference them in the file.
		if !maybeNodeIds.Valid {
			log.Println("invalid way, nodes not included in file", wayid)
			return
		}

		// convert sql.NullString to string
		if val, err := maybeNodeIds.Value(); err == nil {
			nodeids = val.(string)
		} else {
			log.Fatal("invalid nodeid value", wayid)
		}

		var wayNodes = strings.Split(nodeids, ",")
		if len(wayNodes) <= 1 {
			log.Println("found 0 refs for way", wayid)
			return
		}

		var path = geo.NewPath()
		for i, node := range wayNodes {
			coords := strings.Split(node, "#")
			lon, lonErr := strconv.ParseFloat(coords[0], 64)
			lat, latErr := strconv.ParseFloat(coords[1], 64)
			if nil != lonErr || nil != latErr {
				log.Println("error parsing coordinate as float", coords)
				return
			}
			path.InsertAt(i, geo.NewPoint(lon, lat))
		}

		streets = append(streets, &street{Name: name, Path: path})
	})

	return streets
}

func parsePBF(c *cli.Context, conn *sqlite.Connection, path bool) {

	// validate args
	var argv = c.Args()
	if len(argv) != 1 {
		log.Println("invalid arguments, expected: {pbf}")
		os.Exit(1)
	}

	// // create parser handler
	DBHandler := &handler.Sqlite3{Conn: conn}

	// create parser
	parser := parser.NewParser(c.Args()[0])

	// streets handler
	streets := &handler.Streets{
		TagWhitelist: tags.Highway(path),
		NodeMask:     lib.NewBitMask(),
		DBHandler:    DBHandler,
	}

	// parse file
	parser.Parse(streets)

	// reset file
	parser.Reset()

	// create a proxy to filter elements by mask
	filterNodes := &proxy.WhiteList{
		Handler:  DBHandler,
		NodeMask: streets.NodeMask,
	}

	// parse file again
	parser.Parse(filterNodes)
}
